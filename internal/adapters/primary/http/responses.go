package http

import (
	"github.com/gin-gonic/gin"
)

// ErrorResponse estandariza el formato JSON para todos los errores 4xx y 5xx.
// Swagger leerá esta estructura para generar la documentación del contrato.
type ErrorResponse struct {
	ErrorCode string `json:"errorCode" example:"INVALID_REQUEST"`
	Message   string `json:"message" example:"Mensaje descriptivo del error."`
}

// SendError es un helper para enviar respuestas de error consistentes.
func SendError(c *gin.Context, statusCode int, errorCode, message string) {
	c.JSON(statusCode, ErrorResponse{
		ErrorCode: errorCode,
		Message:   message,
	})
}
