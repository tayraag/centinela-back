package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
)

type AuthHandler struct {
	auth ports.AuthService
}

func NewAuthHandler(auth ports.AuthService) *AuthHandler {
	return &AuthHandler{auth: auth}
}

func RegisterAuthRoutes(mux *http.ServeMux, handler *AuthHandler) {
	mux.HandleFunc("POST /auth/login", handler.login)
	mux.HandleFunc("POST /auth/2fa/setup", handler.setupTwoFactor)
	mux.HandleFunc("POST /auth/2fa/verify", handler.verifyTwoFactor)
	mux.HandleFunc("POST /auth/refresh", handler.refresh)
	mux.HandleFunc("POST /auth/logout", handler.logout)
}

type loginRequest struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	RememberSession bool   `json:"recordarSesion"`
}

func (handler *AuthHandler) login(response http.ResponseWriter, request *http.Request) {
	var body loginRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	result, err := handler.auth.Login(request.Context(), ports.LoginRequest{Email: body.Email, Password: body.Password, RememberSession: body.RememberSession})
	if err != nil {
		writeAuthError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *AuthHandler) setupTwoFactor(response http.ResponseWriter, request *http.Request) {
	var body ports.TwoFactorSetupRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	result, err := handler.auth.SetupTwoFactor(request.Context(), body)
	if err != nil {
		writeAuthError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *AuthHandler) verifyTwoFactor(response http.ResponseWriter, request *http.Request) {
	var body ports.TwoFactorVerifyRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	result, err := handler.auth.VerifyTwoFactor(request.Context(), body)
	if err != nil {
		writeAuthError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *AuthHandler) refresh(response http.ResponseWriter, request *http.Request) {
	var body ports.RefreshRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	result, err := handler.auth.Refresh(request.Context(), body)
	if err != nil {
		writeAuthError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *AuthHandler) logout(response http.ResponseWriter, request *http.Request) {
	var body ports.LogoutRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	if err := handler.auth.Logout(request.Context(), body); err != nil {
		writeAuthError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"success": true})
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target interface{}) bool {
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "invalid request body"})
		return false
	}
	return true
}

func writeAuthError(response http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	code := "AUTHENTICATION_ERROR"
	message := "authentication request failed"
	switch {
	case errors.Is(err, services.ErrInvalidCredentials):
		status, code, message = http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid credentials"
	case errors.Is(err, services.ErrTwoFactorInvalid):
		status, code, message = http.StatusUnauthorized, "INVALID_TWO_FACTOR", "invalid two-factor code"
	case errors.Is(err, services.ErrChallengeInvalid):
		status, code, message = http.StatusUnauthorized, "INVALID_CHALLENGE", "invalid or expired challenge"
	case errors.Is(err, services.ErrSessionInvalid):
		status, code, message = http.StatusUnauthorized, "INVALID_SESSION", "invalid session"
	case errors.Is(err, services.ErrAccountInactive):
		status, code, message = http.StatusForbidden, "ACCOUNT_INACTIVE", "account inactive"
	case errors.Is(err, services.ErrPasswordChange):
		status, code, message = http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "password change required"
	}
	writeJSON(response, status, map[string]string{"code": code, "message": message})
}

func writeJSON(response http.ResponseWriter, status int, body interface{}) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}