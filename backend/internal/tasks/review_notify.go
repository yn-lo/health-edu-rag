package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	baseentity "health-edu-rag/internal/domain/base/entity"
	baserepo "health-edu-rag/internal/domain/base/repository"
	wikirepo "health-edu-rag/internal/domain/wiki/repository"
	"health-edu-rag/internal/platform/postgres"
	"health-edu-rag/internal/shared/constants"
)

// HandleReviewNotify 落库一条面向科室管理员的 REVIEW_PENDING 站内通知。
// payload 为 articleID 的十进制字符串；body 取文章标题（best-effort，取不到则为空）。
func HandleReviewNotify(
	ctx context.Context, payload []byte,
	articles *wikirepo.ArticleRepo, notifications *baserepo.NotificationRepo,
) error {
	id, err := strconv.ParseInt(string(payload), 10, 64)
	if err != nil {
		slog.ErrorContext(ctx, "wiki: review notify task invalid payload",
			"payload", string(payload), "err", err)
		return err
	}
	a, gerr := articles.GetByID(ctx, id)
	if gerr != nil {
		// 文章不存在（已删除）→ 无需提醒；读库故障 → 返回错误让 asynq 重试，
		// 不能静默降级为"投超管"（会导致误投且永不重试）。
		if errors.Is(gerr, wikirepo.ErrNotFound) {
			slog.InfoContext(ctx, "wiki: review notify article not found, skip", "article_id", id)
			return nil
		}
		slog.ErrorContext(ctx, "wiki: review notify load article failed", "article_id", id, "err", gerr)
		return gerr
	}
	body := a.Title
	deptID := a.DepartmentID
	refID := strconv.FormatInt(id, 10)
	// 复审逾期是周期性的：把本轮 overdue_at 纳入 ref_id，使"审核通过后再次逾期"能再次提醒，
	// 同时同一轮任务重试仍命中去重。
	if a.ReviewOverdueAt != nil {
		refID = fmt.Sprintf("%d:%d", id, a.ReviewOverdueAt.Unix())
	}
	// 有归属科室时只提醒该科室管理员；科室无启用管理员时回落超管。
	// 不设置 recipient_dept_id 会被当作广播，导致所有科室都看到其他科室的待审文章。
	role := constants.RoleDeptAdmin
	if deptID != nil {
		hasAdmin, herr := notifications.HasActiveRoleInDept(ctx, constants.RoleDeptAdmin, *deptID)
		if herr != nil {
			return herr
		}
		if !hasAdmin {
			role = constants.RoleSuperAdmin
			deptID = nil
		}
	} else {
		role = constants.RoleSuperAdmin
	}
	n := &baseentity.Notification{
		RecipientRole:   role,
		RecipientDeptID: deptID,
		Type:            "REVIEW_PENDING",
		Title:           "文章待审核",
		Body:            body,
		RefID:           &refID,
	}
	if err := notifications.Create(ctx, n); err != nil {
		// 同一文章再次进入待审核时会复用 ref_id；唯一索引 23505 表示已提醒过，按幂等成功处理，
		// 否则 asynq 会重试直至死信，后续真正的提醒也会被阻塞。
		if postgres.IsUniqueViolation(err) {
			slog.InfoContext(ctx, "wiki: review notification already exists, skip", "article_id", id)
			return nil
		}
		slog.ErrorContext(ctx, "wiki: review notify insert failed", "article_id", id, "err", err)
		return err
	}
	slog.InfoContext(ctx, "wiki: review notify created", "article_id", id, "notification_id", n.ID)
	return nil
}
