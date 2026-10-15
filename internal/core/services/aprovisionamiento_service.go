package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

const (
	claveLockAprovisionamiento    = "aprovisionamiento:lock"
	claveReservas                 = "aprovisionamiento:reservas"
	claveCacheEstadoNodo          = "node:status:current"
	ttlLockAprovisionamiento      = time.Minute
	ttlCapacidadAprovisionamiento = 10 * time.Second
	bytesPorGiB                   = int64(1024 * 1024 * 1024)
)

type capacidadAprovisionamiento struct {
	Nodo       ports.NodeStatusDTO         `json:"nodo"`
	Instancias []ports.InstanciaProxmoxDTO `json:"instancias"`
	Storages   []ports.StorageDTO          `json:"storages"`
}

type reservaAprovisionamiento struct {
	ID         string    `json:"id"`
	VMID       int       `json:"vmid,omitempty"`
	Nodo       string    `json:"nodo"`
	StorageID  string    `json:"storageId"`
	Compartido bool      `json:"compartido"`
	Memoria    int64     `json:"memoriaBytes"`
	Disco      int64     `json:"discoBytes"`
	Cores      int       `json:"cores"`
	Vence      time.Time `json:"vence"`
}

// AprovisionamientoService valida capacidad y mantiene una reserva temporal
// compartida antes de delegar la creación en Proxmox. El lock global también
// cubre storages compartidos entre nodos.
type AprovisionamientoService struct {
	proxmox ports.ProxmoxPort
	creador ports.AprovisionamientoPort
	store   ports.KeyValueStore
	ahora   func() time.Time
}

var _ ports.AprovisionamientoPort = (*AprovisionamientoService)(nil)

func NewAprovisionamientoService(proxmox ports.ProxmoxPort, creador ports.AprovisionamientoPort, store ports.KeyValueStore) *AprovisionamientoService {
	return &AprovisionamientoService{proxmox: proxmox, creador: creador, store: store, ahora: time.Now}
}

func (s *AprovisionamientoService) AprovisionarInstancia(ctx context.Context, input ports.CrearInstanciaProxmoxInput) (*ports.ResultadoAprovisionamiento, error) {
	release, err := s.store.AcquireLock(ctx, claveLockAprovisionamiento, ttlLockAprovisionamiento)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	defer func() { _ = release(context.Background()) }()

	reservas, err := s.leerReservas(ctx)
	if err != nil {
		return nil, err
	}
	reservas = reservasVigentes(reservas, s.ahora())

	storage, err := s.validarCapacidad(ctx, input, reservas)
	if err != nil {
		return nil, err
	}

	reserva := reservaAprovisionamiento{
		ID: uuid.NewString(), Nodo: input.Nodo, StorageID: input.StorageID,
		Compartido: storage.Shared == 1, Memoria: int64(input.MemoriaMB) * 1024 * 1024,
		Disco: int64(input.DiscoGB) * bytesPorGiB, Cores: input.Cores,
	}
	reservas = append(reservas, reserva)
	if err := s.guardarReservas(ctx, reservas); err != nil {
		return nil, err
	}

	resultado, err := s.creador.AprovisionarInstancia(ctx, input)
	if err != nil {
		// A transport error does not prove the remote operation was rejected.
		// Keep capacity reserved until reconciliation confirms its disposition.
		s.invalidarCacheCapacidad(context.WithoutCancel(ctx), input.Nodo)
		log.Printf("[APROVISIONAMIENTO] reserva %s retenida en nodo %s: resultado remoto sin confirmar", reserva.ID, reserva.Nodo)
		return nil, err
	}
	reservas[len(reservas)-1].VMID = resultado.VMID
	// Failure to attach the VMID leaves the original conservative reservation.
	// Still return the accepted UPID so the HTTP layer can persist its tracking.
	if err := s.guardarReservas(context.WithoutCancel(ctx), reservas); err != nil {
		log.Printf("[APROVISIONAMIENTO] reserva %s conservada sin asociar VMID %d: %v", reserva.ID, resultado.VMID, err)
	}
	resultado.ReservaID = reserva.ID
	// Al aceptar una creación se invalidan solamente las fotos de capacidad;
	// sesiones, tickets SSE y el resto de Redis no se tocan. La próxima lectura
	// vuelve a Proxmox y la reserva cubre el lapso hasta que refleje el cambio.
	s.invalidarCacheCapacidad(context.WithoutCancel(ctx), input.Nodo)
	return resultado, nil
}

func (s *AprovisionamientoService) validarCapacidad(ctx context.Context, input ports.CrearInstanciaProxmoxInput, reservas []reservaAprovisionamiento) (ports.StorageDTO, error) {
	capacidad, err := s.obtenerCapacidad(ctx, input.Nodo)
	if err != nil {
		return ports.StorageDTO{}, err
	}

	var storage ports.StorageDTO
	encontrado := false
	for _, candidato := range capacidad.Storages {
		if candidato.Storage == input.StorageID {
			storage, encontrado = candidato, true
			break
		}
	}
	contenidoNecesario := "images"
	if input.Tipo == ports.TipoInstanciaLXC {
		contenidoNecesario = "rootdir"
	}
	if !encontrado || storage.Active != 1 || storage.Enabled != 1 || !contieneTipoStorage(storage.Content, contenidoNecesario) {
		return ports.StorageDTO{}, fmt.Errorf("%w: el storage %q no está disponible para %s", ports.ErrRecursosInsuficientes, input.StorageID, input.Tipo)
	}

	var memoriaAsignada int64
	var coresAsignados int
	for _, instancia := range capacidad.Instancias {
		if instancia.Nodo == input.Nodo {
			memoriaAsignada += instancia.MaxMem
			coresAsignados += instancia.MaxCpu
		}
	}
	var memoriaReservada, discoReservado int64
	var coresReservados int
	for _, reserva := range reservas {
		if reserva.Nodo == input.Nodo {
			var memoriaVisible int64
			var coresVisibles int
			for _, instancia := range capacidad.Instancias {
				if reserva.VMID != 0 && instancia.Vmid == reserva.VMID && instancia.Nodo == reserva.Nodo {
					memoriaVisible = instancia.MaxMem
					coresVisibles = instancia.MaxCpu
					break
				}
			}
			// Only subtract the part not already represented by the inventory.
			memoriaReservada += max(int64(0), reserva.Memoria-memoriaVisible)
			coresReservados += max(0, reserva.Cores-coresVisibles)
		}
		if reserva.StorageID == input.StorageID && (storage.Shared == 1 || reserva.Nodo == input.Nodo) {
			discoReservado += reserva.Disco
		}
	}

	memoriaComprometida := memoriaAsignada
	if capacidad.Nodo.MemUsada > memoriaComprometida {
		memoriaComprometida = capacidad.Nodo.MemUsada
	}
	memoriaPedida := int64(input.MemoriaMB) * 1024 * 1024
	discoPedido := int64(input.DiscoGB) * bytesPorGiB
	if memoriaPedida > capacidad.Nodo.MemTotal-memoriaComprometida-memoriaReservada {
		return ports.StorageDTO{}, fmt.Errorf("%w: RAM disponible insuficiente en el nodo %s", ports.ErrRecursosInsuficientes, input.Nodo)
	}
	if input.Cores > capacidad.Nodo.CPUs-coresAsignados-coresReservados {
		return ports.StorageDTO{}, fmt.Errorf("%w: vCPU disponibles insuficientes en el nodo %s", ports.ErrRecursosInsuficientes, input.Nodo)
	}
	if discoPedido > storage.Avail-discoReservado {
		return ports.StorageDTO{}, fmt.Errorf("%w: espacio insuficiente en el storage %s", ports.ErrRecursosInsuficientes, input.StorageID)
	}
	return storage, nil
}

func (s *AprovisionamientoService) obtenerCapacidad(ctx context.Context, nodo string) (*capacidadAprovisionamiento, error) {
	clave := claveCapacidadNodo(nodo)
	if crudo, err := s.store.Get(ctx, clave); err == nil {
		var capacidad capacidadAprovisionamiento
		if json.Unmarshal([]byte(crudo), &capacidad) == nil {
			return &capacidad, nil
		}
	} else if !errors.Is(err, ports.ErrClaveNoEncontrada) {
		return nil, fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}

	estado, err := s.proxmox.ObtenerEstadoNodo(ctx, nodo)
	if err != nil {
		return nil, err
	}
	instancias, err := s.proxmox.ListarInstancias(ctx)
	if err != nil {
		return nil, err
	}
	storages, err := s.proxmox.ObtenerStorages(ctx, nodo)
	if err != nil {
		return nil, err
	}
	capacidad := &capacidadAprovisionamiento{Nodo: *estado, Instancias: instancias, Storages: storages}
	if err := s.store.Set(ctx, clave, capacidad, ttlCapacidadAprovisionamiento); err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	return capacidad, nil
}

// FinalizarReserva releases capacity only after confirmed successful completion.
// Failed or timed-out jobs may still own partially created resources.
func (s *AprovisionamientoService) FinalizarReserva(ctx context.Context, reservaID string, resultado ResultadoTarea) error {
	if resultado.Estado != ports.TareaCompleted {
		return nil
	}
	return s.LiberarReserva(ctx, reservaID)
}

// LiberarReserva requires a confirmed disposition of the committed resources.
// Unknown results must be reconciled instead of expiring automatically.
func (s *AprovisionamientoService) LiberarReserva(ctx context.Context, reservaID string) error {
	if strings.TrimSpace(reservaID) == "" {
		return nil
	}
	release, err := s.store.AcquireLock(ctx, claveLockAprovisionamiento, ttlLockAprovisionamiento)
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	defer func() { _ = release(context.Background()) }()
	reservas, err := s.leerReservas(ctx)
	if err != nil {
		return err
	}
	vigentes := reservasVigentes(reservas, s.ahora())
	restantes, nodo := quitarReservaYObtenerNodo(vigentes, reservaID)
	if err := s.guardarReservas(ctx, restantes); err != nil {
		return err
	}
	if nodo == "" {
		// Compatibilidad con tareas antiguas que no persistieron una reserva.
		_ = s.store.Del(context.WithoutCancel(ctx), claveCacheEstadoNodo)
	} else {
		s.invalidarCacheCapacidad(context.WithoutCancel(ctx), nodo)
	}
	return nil
}

func (s *AprovisionamientoService) invalidarCacheCapacidad(ctx context.Context, nodo string) {
	_ = s.store.Del(ctx, claveCacheEstadoNodo, claveCapacidadNodo(nodo))
}

func claveCapacidadNodo(nodo string) string {
	return "aprovisionamiento:capacidad:" + strings.TrimSpace(nodo)
}

func (s *AprovisionamientoService) leerReservas(ctx context.Context) ([]reservaAprovisionamiento, error) {
	crudo, err := s.store.Get(ctx, claveReservas)
	if errors.Is(err, ports.ErrClaveNoEncontrada) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	var reservas []reservaAprovisionamiento
	if err := json.Unmarshal([]byte(crudo), &reservas); err != nil {
		return nil, fmt.Errorf("%w: reservas inválidas: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	return reservas, nil
}

func (s *AprovisionamientoService) guardarReservas(ctx context.Context, reservas []reservaAprovisionamiento) error {
	if len(reservas) == 0 {
		if err := s.store.Del(ctx, claveReservas); err != nil {
			return fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
		}
		return nil
	}
	if err := s.store.Set(ctx, claveReservas, reservas, 0); err != nil {
		return fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	return nil
}

func reservasVigentes(reservas []reservaAprovisionamiento, ahora time.Time) []reservaAprovisionamiento {
	vigentes := reservas[:0]
	for _, reserva := range reservas {
		// Legacy expiry timestamps do not establish that remote work stopped.
		vigentes = append(vigentes, reserva)
	}
	return vigentes
}

func quitarReserva(reservas []reservaAprovisionamiento, id string) []reservaAprovisionamiento {
	resultado := reservas[:0]
	for _, reserva := range reservas {
		if reserva.ID != id {
			resultado = append(resultado, reserva)
		}
	}
	return resultado
}

func quitarReservaYObtenerNodo(reservas []reservaAprovisionamiento, id string) ([]reservaAprovisionamiento, string) {
	resultado := reservas[:0]
	nodo := ""
	for _, reserva := range reservas {
		if reserva.ID == id {
			nodo = reserva.Nodo
			continue
		}
		resultado = append(resultado, reserva)
	}
	return resultado, nodo
}

func contieneTipoStorage(contenidos, requerido string) bool {
	for _, contenido := range strings.Split(contenidos, ",") {
		if strings.TrimSpace(contenido) == requerido {
			return true
		}
	}
	return false
}
