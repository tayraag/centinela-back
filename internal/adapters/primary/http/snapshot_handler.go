package http

import (
	"context"
	"errors"
	"net/http"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SnapshotHandler expone los puntos de restauración de una instancia (RF-06).
type SnapshotHandler struct {
	service ports.SnapshotService
}

// NewSnapshotHandler crea el handler de snapshots.
func NewSnapshotHandler(service ports.SnapshotService) *SnapshotHandler {
	return &SnapshotHandler{service: service}
}

// SnapshotResponse es un snapshot de la instancia.
type SnapshotResponse struct {
	Name        string `json:"name" binding:"required" example:"snap_pre_upgrade"`
	SnapTime    int64  `json:"snaptime" binding:"required" example:"1774358400"` // segundos UNIX
	Description string `json:"description" binding:"required" example:"Punto previo a upgrade"`
	Parent      string `json:"parent" binding:"required" example:""` // snapshot anterior; "" si es el primero
	VMState     bool   `json:"vmstate" binding:"required" example:"false"`
}

// CrearSnapshotRequest es el cuerpo de POST /api/instances/:instanceRef/snapshots.
type CrearSnapshotRequest struct {
	Snapname    string `json:"snapname" binding:"required" example:"snap_pre_upgrade"`
	Description string `json:"description" example:"Punto previo a upgrade"`
}

// SnapshotAceptadoResponse es el 202 de la creación: tareaId llega también en
// TASK_FINISHED (accion SNAPSHOT). El UPID de Proxmox nunca sale de la API.
type SnapshotAceptadoResponse struct {
	TareaID  string `json:"tareaId" binding:"required" example:"0192e4a1-4f89-7e23-9a0c-0305e82c3301"`
	Snapname string `json:"snapname" binding:"required" example:"snap_pre_upgrade"`
}

// ==========================================
// GET /api/instances/:instanceRef/snapshots
// ==========================================

// ListarSnapshots devuelve los snapshots de la instancia.
//
// @Summary      Listar snapshots
// @Description  OPERATIVO. Devuelve los snapshots de la instancia, del más nuevo al más viejo (por `snaptime`). `parent` es el snapshot anterior en la cadena ("" si es el primero); `vmstate` indica si guardó la RAM (solo VM). No incluye la entrada "current" de Proxmox (el estado actual). Un OPERATOR necesita al menos READ_ONLY sobre la instancia.
// @Tags         Snapshots
// @Produce      json
// @Security     BearerAuth
// @Param        instanceRef path string true "Referencia pública de la instancia (inst_...)"
// @Success      200 {array} SnapshotResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene permiso sobre la instancia"
// @Failure      404 {object} ErrorResponse "RESOURCE_NOT_FOUND — referencia inválida o instancia inexistente (misma respuesta)"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR | PROXMOX_ERROR"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /instances/{instanceRef}/snapshots [get]
func (h *SnapshotHandler) ListarSnapshots(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	snapshots, err := h.service.Listar(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	respuesta := make([]SnapshotResponse, 0, len(snapshots))
	for _, s := range snapshots {
		respuesta = append(respuesta, SnapshotResponse{Name: s.Nombre, SnapTime: s.SnapTime, Description: s.Descripcion, Parent: s.Padre, VMState: s.VMState})
	}
	c.JSON(http.StatusOK, respuesta)
}

// ==========================================
// POST /api/instances/:instanceRef/snapshots
// ==========================================

// CrearSnapshot crea un snapshot de la instancia de forma asíncrona.
//
// @Summary      Crear snapshot
// @Description  OPERATIVO. Crea un punto de restauración (sin guardar la RAM). Requiere ADMIN o FULL_ACCESS. `snapname` debe empezar con letra y tener solo letras, números, `_` y `-`, de 2 a 40 caracteres (no puede ser `current`); si no, 400 INVALID_SNAPSHOT_NAME sin contactar a Proxmox. `description` es opcional, hasta 255 caracteres. La operación es asíncrona: responde 202 con `tareaId` (el mismo que llega en TASK_FINISHED con `accion: "SNAPSHOT"`) y `snapname`; el UPID de Proxmox nunca se devuelve. Las instancias de infraestructura también admiten snapshots. Lleva la exclusión mutua: con otra operación en curso responde 409 INSTANCE_BUSY.
// @Tags         Snapshots
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        instanceRef path string               true "Referencia pública de la instancia (inst_...)"
// @Param        body        body CrearSnapshotRequest true "Nombre y descripción del snapshot"
// @Success      202 {object} SnapshotAceptadoResponse
// @Failure      400 {object} ErrorResponse "INVALID_SNAPSHOT_NAME — nombre con espacios, caracteres especiales, que no empieza con letra, fuera de 2 a 40 caracteres o \"current\" | INVALID_REQUEST — cuerpo inválido o descripción de más de 255 caracteres"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS (READ_ONLY solo lista)"
// @Failure      404 {object} ErrorResponse "RESOURCE_NOT_FOUND — referencia inválida o instancia inexistente (misma respuesta)"
// @Failure      409 {object} ErrorResponse "SNAPSHOT_ALREADY_EXISTS — la instancia ya tiene un snapshot con ese nombre | INSTANCE_BUSY — la instancia está ejecutando otra tarea"
// @Failure      500 {object} ErrorResponse "TASK_TRACKING_UNAVAILABLE — Proxmox aceptó el snapshot pero no se pudo registrar su seguimiento; verificar antes de reintentar | PROXMOX_ERROR"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      503 {object} ErrorResponse "INSTANCE_LOCK_UNAVAILABLE — no se pudo verificar si la instancia está ocupada"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — el snapshot puede haberse creado"
// @Router       /instances/{instanceRef}/snapshots [post]
func (h *SnapshotHandler) CrearSnapshot(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	var req CrearSnapshotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Se requiere un JSON con snapname (y opcionalmente description).")
		return
	}
	creado, err := h.service.Crear(c.Request.Context(), extraerUserID(c), vmid, req.Snapname, req.Description)
	switch {
	case errors.Is(err, ports.ErrSnapshotNombreInvalido):
		SendError(c, http.StatusBadRequest, "INVALID_SNAPSHOT_NAME", "El nombre debe empezar con una letra y tener solo letras, números, guion y guion bajo, de 2 a 40 caracteres (no puede ser \"current\").")
	case errors.Is(err, ports.ErrSnapshotDescripcionInvalida):
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "La descripción no puede superar los 255 caracteres.")
	case errors.Is(err, ports.ErrSnapshotExistente):
		SendError(c, http.StatusConflict, "SNAPSHOT_ALREADY_EXISTS", "La instancia ya tiene un snapshot con ese nombre.")
	case errors.Is(err, ports.ErrSeguimientoNoRegistrado):
		SendError(c, http.StatusInternalServerError, "TASK_TRACKING_UNAVAILABLE", "Proxmox aceptó el snapshot, pero no se pudo registrar su seguimiento. Verifique el estado antes de reintentar.")
	case err != nil:
		mapearErrorProxmox(c, err)
	default:
		c.JSON(http.StatusAccepted, SnapshotAceptadoResponse{TareaID: creado.TareaID.String(), Snapname: creado.Nombre})
	}
}

// ==========================================
// POST /api/instances/:instanceRef/snapshots/:snapname/rollback
// DELETE /api/instances/:instanceRef/snapshots/:snapname
// ==========================================

// RevertirSnapshot vuelve la instancia al estado del snapshot (asíncrono).
//
// @Summary      Revertir a un snapshot (rollback)
// @Description  OPERATIVO. Vuelve la instancia al estado del snapshot; Proxmox la deja apagada. Requiere ADMIN o FULL_ACCESS y lleva la exclusión mutua: si la instancia tiene una tarea RUNNING o el lock de operación está tomado responde 409 INSTANCE_BUSY sin contactar a Proxmox. Se registra en tareas_asincronas con accion ROLLBACK_SNAPSHOT y responde 202 con `tareaId` (el UPID de Proxmox nunca se devuelve). Mientras dura, el inventario y el detalle muestran la instancia con status `rolling_back` y activeTask.action `ROLLBACK_SNAPSHOT`; el desenlace llega en TASK_FINISHED (accion ROLLBACK_SNAPSHOT). Las instancias de infraestructura también lo admiten.
// @Tags         Snapshots
// @Produce      json
// @Security     BearerAuth
// @Param        instanceRef path string true "Referencia pública de la instancia (inst_...)"
// @Param        snapname    path string true "Nombre del snapshot"
// @Success      202 {object} AccionAceptadaResponse
// @Failure      400 {object} ErrorResponse "INVALID_SNAPSHOT_NAME — el nombre no cumple el formato de Proxmox"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS"
// @Failure      404 {object} ErrorResponse "RESOURCE_NOT_FOUND — referencia inválida o instancia inexistente | SNAPSHOT_NOT_FOUND — la instancia no tiene ese snapshot"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY — la instancia está ejecutando otra tarea; no se contactó a Proxmox"
// @Failure      500 {object} ErrorResponse "TASK_TRACKING_UNAVAILABLE | PROXMOX_ERROR"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      503 {object} ErrorResponse "INSTANCE_LOCK_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — el rollback puede haberse aplicado"
// @Router       /instances/{instanceRef}/snapshots/{snapname}/rollback [post]
func (h *SnapshotHandler) RevertirSnapshot(c *gin.Context) {
	h.ordenSobreSnapshot(c, h.service.Revertir)
}

// EliminarSnapshot borra un snapshot de la instancia (asíncrono).
//
// @Summary      Eliminar un snapshot
// @Description  OPERATIVO. Borra el snapshot; la instancia no cambia. Requiere ADMIN o FULL_ACCESS y lleva la exclusión mutua (409 INSTANCE_BUSY sin contactar a Proxmox si hay otra operación en curso). Se registra en tareas_asincronas con accion DELETE_SNAPSHOT y responde 202 con `tareaId`; el desenlace llega en TASK_FINISHED (accion DELETE_SNAPSHOT). Las instancias de infraestructura también lo admiten.
// @Tags         Snapshots
// @Produce      json
// @Security     BearerAuth
// @Param        instanceRef path string true "Referencia pública de la instancia (inst_...)"
// @Param        snapname    path string true "Nombre del snapshot"
// @Success      202 {object} AccionAceptadaResponse
// @Failure      400 {object} ErrorResponse "INVALID_SNAPSHOT_NAME — el nombre no cumple el formato de Proxmox"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS"
// @Failure      404 {object} ErrorResponse "RESOURCE_NOT_FOUND — referencia inválida o instancia inexistente | SNAPSHOT_NOT_FOUND — la instancia no tiene ese snapshot"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY — la instancia está ejecutando otra tarea; no se contactó a Proxmox"
// @Failure      500 {object} ErrorResponse "TASK_TRACKING_UNAVAILABLE | PROXMOX_ERROR"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      503 {object} ErrorResponse "INSTANCE_LOCK_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — el borrado puede haberse aplicado"
// @Router       /instances/{instanceRef}/snapshots/{snapname} [delete]
func (h *SnapshotHandler) EliminarSnapshot(c *gin.Context) {
	h.ordenSobreSnapshot(c, h.service.Eliminar)
}

func (h *SnapshotHandler) ordenSobreSnapshot(c *gin.Context, orden func(ctx context.Context, usuarioID uuid.UUID, vmid int, nombre string) (uuid.UUID, error)) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	tareaID, err := orden(c.Request.Context(), extraerUserID(c), vmid, c.Param("snapname"))
	switch {
	case errors.Is(err, ports.ErrSnapshotNombreInvalido):
		SendError(c, http.StatusBadRequest, "INVALID_SNAPSHOT_NAME", "El nombre debe empezar con una letra y tener solo letras, números, guion y guion bajo, de 2 a 40 caracteres (no puede ser \"current\").")
	case errors.Is(err, ports.ErrSnapshotNoEncontrado):
		SendError(c, http.StatusNotFound, "SNAPSHOT_NOT_FOUND", "La instancia no tiene un snapshot con ese nombre.")
	case errors.Is(err, ports.ErrSeguimientoNoRegistrado):
		SendError(c, http.StatusInternalServerError, "TASK_TRACKING_UNAVAILABLE", "Proxmox aceptó la orden, pero no se pudo registrar su seguimiento. Verifique el estado antes de reintentar.")
	case err != nil:
		mapearErrorProxmox(c, err)
	default:
		c.JSON(http.StatusAccepted, AccionAceptadaResponse{TareaID: tareaID.String()})
	}
}
