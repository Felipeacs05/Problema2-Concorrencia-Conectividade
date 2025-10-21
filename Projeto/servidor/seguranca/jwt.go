package seguranca

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"jogodistribuido/servidor/tipos"
	"strings"
	"time"
)

const (
	JWT_SECRET     = "jogo_distribuido_secret_key_2025"
	JWT_EXPIRATION = 24 * time.Hour
)

// GenerateJWT gera um token JWT para autenticação entre servidores
func GenerateJWT(serverID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

	payload := map[string]interface{}{
		"server_id": serverID,
		"exp":       time.Now().Add(JWT_EXPIRATION).Unix(),
		"iat":       time.Now().Unix(),
	}
	payloadJSON, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	message := header + "." + payloadB64
	signature := GenerateHMAC(message, JWT_SECRET)

	return message + "." + signature
}

// ValidateJWT valida um token JWT
func ValidateJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("token inválido")
	}

	message := parts[0] + "." + parts[1]
	expectedSig := GenerateHMAC(message, JWT_SECRET)

	if parts[2] != expectedSig {
		return "", fmt.Errorf("assinatura inválida")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("payload inválido")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return "", fmt.Errorf("payload JSON inválido")
	}

	exp, ok := payload["exp"].(float64)
	if !ok || time.Now().Unix() > int64(exp) {
		return "", fmt.Errorf("token expirado")
	}

	serverID, ok := payload["server_id"].(string)
	if !ok {
		return "", fmt.Errorf("server_id ausente")
	}

	return serverID, nil
}

// GenerateHMAC gera uma assinatura HMAC-SHA256
func GenerateHMAC(message, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// SignEvent assina um evento de jogo
func SignEvent(event *tipos.GameEvent) {
	data := fmt.Sprintf("%d:%s:%s:%s", event.EventSeq, event.MatchID, event.EventType, event.PlayerID)
	event.Signature = GenerateHMAC(data, JWT_SECRET)
}

// VerifyEventSignature verifica a assinatura de um evento
func VerifyEventSignature(event *tipos.GameEvent) bool {
	data := fmt.Sprintf("%d:%s:%s:%s", event.EventSeq, event.MatchID, event.EventType, event.PlayerID)
	expectedSig := GenerateHMAC(data, JWT_SECRET)
	return event.Signature == expectedSig
}

func MustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
