package handler

import (
	"net/http"

	"health-edu-rag/internal/domain/base/service"
	"health-edu-rag/internal/shared/identity"
)

// actorFromRequest 从 JWT 中间件写入的 context 提取操作者身份。
func actorFromRequest(r *http.Request) (service.Actor, error) {
	return identity.ActorFromRequest(r)
}
