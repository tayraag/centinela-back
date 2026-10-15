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
	claveLockAprovisionamiento    = "lock:instance:provisioning"
	claveReservas                 = "aprovisionamiento:reservas"
	claveCacheEstadoNodo          = "node:status:current"
	ttlLockAprovisionamiento      = 30 * time.Second
	esperaLockAprovisionamiento   = 2 * time.Second
	ttlCapacidadAprovisionamiento = 10 * time.Second
	bytesPorMiB                   = int64(1024 * 1024)
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

// CuotasService valida CPU, RAM y storage bajo un lock distribuido y mantiene
// una reserva hasta que Proxmox confirme el resultado de la creación.
// El lock global también cubre storages compartidos entre nodos.
type CuotasService struct {
	proxmox ports.ProxmoxPort
	creador ports.AprovisionamientoPort
	store   ports.KeyValueStore
	ahora   func() time.Time
}

var _ ports.AprovisionamientoPort = (*CuotasService)(nil)

func NewCuotasService(proxmox ports.ProxmoxPort, creador ports.AprovisionamientoPort, store ports.KeyValueStore) *CuotasService {
	return &CuotasService{proxmox: proxmox, creador: creador, store: store, ahora: time.Now}
}

func (s *CuotasService) AprovisionarInstancia(ctx context.Context, input ports.CrearInstanciaProxmoxInput) (*ports.ResultadoAprovisionamiento, error) {
	memoriaPedida, memoriaValida := cantidadEnBytes(input.MemoriaMB, bytesPorMiB)
	discoPedido, discoValido := cantidadEnBytes(input.DiscoGB, bytesPorGiB)
	if !memoriaValida || !discoValido {
		return nil, fmt.Errorf("%w: tamaño de RAM o disco fuera de rango", ports.ErrRecursosInsuficientes)
	}

	lockCtx, cancelarEspera := context.WithTimeout(ctx, esperaLockAprovisionamiento)
	release, err := s.store.AcquireLock(lockCtx, claveLockAprovisionamiento, ttlLockAprovisionamiento)
	cancelarEspera()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("%w: otra creación está reservando capacidad; reintente", ports.ErrRecursosInsuficientes)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrCoordinacionAprovisionamiento, err)
	}
	defer func() { _ = release(context.Background()) }()

	reservas, err := s.leerReservas(ctx)
	if err != nil {
		return nil, err
	}
	reservas = reservasVigentes(reservas, s.ahora())

	storage, err := s.validarCapacidad(ctx, input, reservas, memoriaPedida, discoPedido)
	if err != nil {
		return nil, err
	}

	reserva := reservaAprovisionamiento{
		ID: uuid.NewString(), Nodo: input.Nodo, StorageID: input.StorageID,
		Compartido: storage.Shared == 1, Memoria: memoriaPedida,
		Disco: discoPedido, Cores: input.Cores,
	}
	reservas = append(reservas, reserva)
	if err := s.guardarReservas(ctx, reservas); err != nil {
		return nil, err
	}

	resultado, err := s.creador.AprovisionarInstancia(ctx, input)
	if err != nil {
		if errors.Is(err, ports.ErrCreacionNoDespachada) {
			if quitarErr := s.guardarReservas(context.WithoutCancel(ctx), quitarReserva(reservas, reserva.ID)); quitarErr != nil {
				return nil, errors.Join(err, quitarErr)
			}
			s.invalidarCacheCapacidad(context.WithoutCancel(ctx), input.Nodo)
			return nil, err
		}
		// Un error posterior al despacho no demuestra que Proxmox haya rechazado
		// la operación. Se retiene la capacidad hasta reconciliar el resultado.
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

func (s *CuotasService) validarCapacidad(ctx context.Context, input ports.CrearInstanciaProxmoxInput, reservas []reservaAprovisionamiento, memoriaPedida, discoPedido int64) (ports.StorageDTO, error) {
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
	memoriaReservadaHost := porcentajeRedondeadoHaciaArriba(max(int64(0), capacidad.Nodo.MemTotal), headroomRAMPercent)
	memoriaDisponibleSegura := max(int64(0), capacidad.Nodo.MemTotal-memoriaComprometida-memoriaReservadaHost-memoriaReservada)
	if memoriaPedida > memoriaDisponibleSegura {
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

func cantidadEnBytes(cantidad int, bytesPorUnidad int64) (int64, bool) {
	if cantidad < 0 || bytesPorUnidad <= 0 {
		return 0, false
	}
	cantidadInt64 := int64(cantidad)
	maxInt64 := int64(^uint64(0) >> 1)
	if cantidadInt64 > maxInt64/bytesPorUnidad {
		return 0, false
	}
	return cantidadInt64 * bytesPorUnidad, true
}

func (s *CuotasService) obtenerCapacidad(ctx context.Context, nodo string) (*capacidadAprovisionamiento, error) {
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

// FinalizarReserva libera capacidad únicamente cuando Proxmox confirmó que la
// tarea terminó correctamente. Un fallo o timeout puede haber dejado recursos.
func (s *CuotasService) FinalizarReserva(ctx context.Context, reservaID string, resultado ResultadoTarea) error {
	if resultado.Estado != ports.TareaCompleted {
		return nil
	}
	return s.LiberarReserva(ctx, reservaID)
}

// LiberarReserva requiere un desenlace confirmado. Los resultados inciertos se
// conservan para reconciliarlos en lugar de vencer automáticamente.
func (s *CuotasService) LiberarReserva(ctx context.Context, reservaID string) error {
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

func (s *CuotasService) invalidarCacheCapacidad(ctx context.Context, nodo string) {
	_ = s.store.Del(ctx, claveCacheEstadoNodo, claveCapacidadNodo(nodo))
}

func claveCapacidadNodo(nodo string) string {
	return "aprovisionamiento:capacidad:" + strings.TrimSpace(nodo)
}

func (s *CuotasService) leerReservas(ctx context.Context) ([]reservaAprovisionamiento, error) {
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

func (s *CuotasService) guardarReservas(ctx context.Context, reservas []reservaAprovisionamiento) error {
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
