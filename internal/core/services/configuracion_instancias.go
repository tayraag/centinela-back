package services

import (
	"context"
	"fmt"

	"el-centinela/internal/core/ports"
)

// ampliadorCapacidad es la parte de CuotasService que usa la reconfiguración.
type ampliadorCapacidad interface {
	AmpliarInstancia(ctx context.Context, nodo string, cores int, deltaMemoria int64, aplicar func() error) error
}

type configuracionInstancias struct {
	proxmox ports.ProxmoxPort
	cuotas  ampliadorCapacidad
}

// NewConfiguracionInstancias crea el servicio de cambio de CPU y RAM.
func NewConfiguracionInstancias(proxmox ports.ProxmoxPort, cuotas ampliadorCapacidad) ports.ConfiguracionInstancias {
	return &configuracionInstancias{proxmox: proxmox, cuotas: cuotas}
}

// Reconfigurar:
//  1. Lee la instancia (nodo, tipo, estado) y la config en uso.
//  2. Calcula los incrementos contra lo que la instancia usa hoy (RF-10). Si la
//     RAM sube (DeltaRAM > 0), CuotasService valida el incremento contra el
//     margen del nodo y aplica con el lock de capacidad tomado. Si suben las
//     vCPU, solo se controla que la instancia no supere los hilos lógicos del
//     nodo: se admite CPU overcommit, nunca se suman las vCPU de las demás. Si
//     no se cumple, no se escribe nada en Proxmox. Las reducciones no se validan.
//  3. Hace el PUT /config con el digest leído (si alguien cambió la config en el
//     medio, Proxmox lo rechaza).
//  4. Si la instancia está encendida, vuelve a leer la config en uso: si no
//     tiene los valores pedidos, Proxmox los dejó pendientes (sin hot-plug) y
//     hace falta reiniciar. Apagada, el cambio ya quedó aplicado.
func (s *configuracionInstancias) Reconfigurar(ctx context.Context, vmid, cores, memoriaMB int) (*ports.ResultadoReconfiguracion, error) {
	inst, err := s.proxmox.ObtenerInstancia(ctx, vmid)
	if err != nil {
		return nil, err
	}
	enUso, err := s.proxmox.ObtenerConfig(ctx, inst.Nodo, inst.Tipo, vmid, true)
	if err != nil {
		return nil, err
	}
	conPendientes, err := s.proxmox.ObtenerConfig(ctx, inst.Nodo, inst.Tipo, vmid, false)
	if err != nil {
		return nil, err
	}
	nueva := ports.ConfigInstanciaDTO{Cores: enUso.Cores, MemoriaMB: enUso.MemoriaMB}
	if cores > 0 {
		nueva.Cores = cores
	}
	if memoriaMB > 0 {
		nueva.MemoriaMB = memoriaMB
	}
	recursoTipo := ports.RecursoVM
	if inst.Tipo == ports.TipoInstanciaLXC {
		recursoTipo = ports.RecursoLXC
	}
	resultado := &ports.ResultadoReconfiguracion{
		RecursoTipo: recursoTipo,
		Anterior:    ports.ConfigInstanciaDTO{Cores: enUso.Cores, MemoriaMB: enUso.MemoriaMB},
		Solicitada:  nueva,
	}

	// Los incrementos se miden contra lo que el nodo ya tiene contado para la
	// instancia (MaxCpu y MaxMem del inventario), igual que en la creación.
	var coresQueSuben int
	var deltaMemoria int64
	if cores > inst.MaxCpu {
		coresQueSuben = cores
	}
	if memoriaMB > 0 {
		deltaMemoria = int64(memoriaMB)*bytesPorMiB - inst.MaxMem
	}
	aplicar := func() error {
		return s.proxmox.ActualizarConfig(ctx, inst.Nodo, inst.Tipo, vmid, cores, memoriaMB, conPendientes.Digest)
	}
	if coresQueSuben > 0 || deltaMemoria > 0 {
		err = s.cuotas.AmpliarInstancia(ctx, inst.Nodo, coresQueSuben, max(int64(0), deltaMemoria), aplicar)
	} else {
		err = aplicar()
	}
	if err != nil {
		// Conservar el diferencial para auditar también los rechazos del PUT de
		// Proxmox. Los errores anteriores a leer la configuración retornan nil.
		return resultado, err
	}
	resultado.Aplicada = true

	if inst.Estado != "running" {
		return resultado, nil
	}
	ahora, err := s.proxmox.ObtenerConfig(ctx, inst.Nodo, inst.Tipo, vmid, true)
	if err != nil {
		resultado.VerificacionPendiente = true
		return resultado, fmt.Errorf("%w: %v", ports.ErrVerificacionPostConfig, err)
	}
	resultado.RequiresReboot = (cores > 0 && ahora.Cores != cores) || (memoriaMB > 0 && ahora.MemoriaMB != memoriaMB)
	return resultado, nil
}
