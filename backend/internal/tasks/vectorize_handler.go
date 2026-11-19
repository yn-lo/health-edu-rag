package tasks

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"health-edu-rag/internal/domain/wiki/entity"
	"health-edu-rag/internal/domain/wiki/repository"
	wikiservice "health-edu-rag/internal/domain/wiki/service"
	"health-edu-rag/internal/platform/llm"
	"health-edu-rag/internal/shared/constants"
	"health-edu-rag/internal/shared/contenthash"

	asynqlib "github.com/hibiken/asynq"
	"github.com/pgvector/pgvector-go"
)

// articleFetcher 暴露 handler 所需的 article 读取与行锁版本读取能力（便于测试 mock）。
type articleFetcher interface {
	GetByID(ctx context.Context, id int64) (*entity.Article, error)
	// LockPublishedVersion 事务内锁定文章行并返回当前已发布快照版本（FOR UPDATE），
	// 见 ArticleRepo.LockPublishedVersion。切片只与 published_version 绑定。
	LockPublishedVersion(ctx context.Context, id int64) (int, error)
}

// chunkWriter 暴露 handler 所需的 chunk 写入能力（便于测试 mock）。
type chunkWriter interface {
	DeactivateByArticle(ctx context.Context, articleID int64) (int64, error)
	DeleteInactiveByArticle(ctx context.Context, articleID int64) (int64, error)
	Create(ctx context.Context, c *entity.ArticleChunk) error
}

// TxRunner 事务执行能力（消费者定义，ISP）。*postgres.TxManager 实现此接口。
type TxRunner interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// VectorizeHandler 处理 asynq TaskVectorizeArticle 任务（REQ-WIKI-012）。
// 流程：解析 articleID → 取已发布文章 → 读 RAG 配置切片参数 → 切片 → embedding
// → 事务内锁定文章复核版本 → 原子替换切片（失效旧 + 删除 + 写入新）。
type VectorizeHandler struct {
	articles articleFetcher
	chunks   chunkWriter
	embed    llm.Embedder
	cfg      wikiservice.RAGConfigProvider
	tx       TxRunner // nil 时退化为无事务写入（仅测试兼容；生产必须注入）
}

// NewVectorizeHandler 构造向量化 handler。
// 接受 *repository.ArticleRepo / *repository.ChunkRepo 具体类型（均满足上述接口）。
// cfg 注入 RAG 配置提供者以动态读取 chunk_size/chunk_overlap；可为 nil（回退 constants 默认值）。
// tx 注入事务管理器，用于切片的原子替换与版本校验。
func NewVectorizeHandler(
	articles *repository.ArticleRepo, chunks *repository.ChunkRepo,
	embed llm.Embedder, cfg wikiservice.RAGConfigProvider, tx TxRunner,
) *VectorizeHandler {
	return &VectorizeHandler{articles: articles, chunks: chunks, embed: embed, cfg: cfg, tx: tx}
}

// resolveChunkConfig 解析切片参数：优先用 RAG 配置，失败或未注入时回退 constants 默认值。
func (h *VectorizeHandler) resolveChunkConfig(ctx context.Context) (size, overlap int) {
	size, overlap = constants.DefaultChunkSize, constants.DefaultChunkOverlap
	if h.cfg == nil {
		return
	}
	cfg, err := h.cfg.GetRAGConfig(ctx)
	if err != nil {
		slog.WarnContext(ctx, "wiki: get rag config for chunk params failed, use defaults", "err", err)
		return
	}
	if cfg == nil {
		return
	}
	if cfg.ChunkSize > 0 {
		size = cfg.ChunkSize
	}
	if cfg.ChunkOverlap >= 0 {
		overlap = cfg.ChunkOverlap
	}
	return
}

// HandleVectorize asynq task handler：payload 为 articleID 的十进制字符串（与 AsynqVectorizeEnqueuer.Enqueue 对齐）。
// 错误策略：
//   - 文章不存在/未发布/已归档：返回 SkipRetry，避免重试死循环。
//   - embedding 失败/DB 写入失败：返回 err 触发 asynq 重试。
func (h *VectorizeHandler) HandleVectorize(ctx context.Context, t *asynqlib.Task) error {
	id, err := strconv.ParseInt(string(t.Payload()), 10, 64)
	if err != nil {
		slog.ErrorContext(ctx, "wiki: vectorize task invalid payload",
			"payload", string(t.Payload()), "err", err)
		return fmt.Errorf("parse articleID %q: %w", string(t.Payload()), asynqlib.SkipRetry)
	}

	// 用 GetByID（不读 GetPublishedByID）避免 worker 重试时叠加 view_count 副作用。
	article, err := h.articles.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			slog.WarnContext(ctx, "wiki: vectorize article not found, skip retry", "article_id", id)
			return fmt.Errorf("article %d not found: %w", id, asynqlib.SkipRetry)
		}
		return fmt.Errorf("get article %d: %w", id, err)
	}
	if article.Status != constants.ArticleStatusPublished {
		slog.InfoContext(ctx, "wiki: vectorize article not published, skip retry",
			"article_id", id, "status", article.Status)
		return fmt.Errorf("article %d status=%s not published: %w",
			id, article.Status, asynqlib.SkipRetry)
	}

	chunkSize, chunkOverlap := h.resolveChunkConfig(ctx)
	// P1：切片必须重建自**已审核发布快照**（published_content + published_version），
	// 而非当前编辑稿。理由有二：
	//   1. 检索按 c.version = a.published_version 绑定审核版本，若按当前 version 写入切片，
	//      纯元数据变更（封面/来源等）递增 version 后，重建产物版本与前缀版本不符 → 整篇被检索排除；
	//   2. 编辑稿可能尚未审核，直接向量化会让他人检索到未审核内容。
	pubContent, pubVersion := article.PublishedContent, article.PublishedVersion
	if pubVersion <= 0 {
		// 兼容无快照的历史行（published_version=0）：退回当前编辑稿，避免清空既有切片。
		slog.WarnContext(ctx, "wiki: article has no published snapshot, fallback to current draft",
			"article_id", id, "version", article.Version)
		pubContent, pubVersion = article.Content, article.Version
	}
	// 切片用纯文本：正文为富文本 HTML，直接切片会把标签、样式与 <img src> URL 一并喂给
	// embedding，成为检索噪声（且图片本身不参与向量化）。
	plainText := htmlToPlainText(pubContent)
	chunkTexts := chunkContent(plainText, chunkSize, chunkOverlap)
	// P1：纯图片/空正文是**有效重建结果**（新版正文不含可检索文本），必须清除旧切片——
	// 直接 return nil 会让已被替换的旧指引持续被检索命中（患者收到过期内容）。
	// 走与正常路径相同的版本复核 + 事务性清除，只是不写入新切片。
	if len(chunkTexts) == 0 {
		if err := h.clearChunks(ctx, id, pubVersion); err != nil {
			return err
		}
		slog.WarnContext(ctx, "wiki: vectorize empty content, cleared stale chunks",
			"article_id", id, "version", pubVersion)
		return nil
	}
	slog.InfoContext(ctx, "wiki: vectorize chunking",
		"article_id", id, "chunk_size", chunkSize, "chunk_overlap", chunkOverlap, "chunks", len(chunkTexts))

	// P2：用**同一客户端快照**完成"生成向量 + 读取模型标识"。
	// EmbedWithModel 在单次调用内固定客户端，消除"读取模型名 / 生成向量"两次 Load 之间
	// 发生热切换导致 A 模型向量被标成 B 模型的问题（向量空间混用、自动重建失效）。
	embeddings, model, err := h.embedWithModel(ctx, chunkTexts)
	if err != nil {
		// LLM 未配置或调用失败：触发 asynq 重试；持续失败由 asynq 兜底进入死信。
		return fmt.Errorf("embed article %d: %w", id, err)
	}
	if len(embeddings) != len(chunkTexts) {
		return fmt.Errorf("embed article %d: vector count mismatch (got %d, want %d)",
			id, len(embeddings), len(chunkTexts))
	}

	// 事务内原子替换切片：锁定文章行复核版本 → 失效旧切片 → 删除已失效 → 写入新切片。
	// embedding 是长耗时操作，期间文章可能已被再次更新（新任务已入队）。若不复核就直接写入，
	// 后完成的旧任务会把新版本切片失效并删除，造成旧内容覆盖新版本（P0 向量版本一致性）。
	// 复核对象为 published_version（切片绑定的版本）：纯元数据变更只递增 version，
	// 不改变已发布快照，不应因此丢弃本次重建（P1）。
	swap := func(ctx context.Context) error {
		if h.tx == nil {
			// 未注入事务管理器（单测）时不具备行锁，仍执行版本复核。
			return h.swapChunks(ctx, id, pubVersion, model, chunkTexts, embeddings)
		}
		return h.tx.WithTx(ctx, func(ctx context.Context) error {
			return h.swapChunks(ctx, id, pubVersion, model, chunkTexts, embeddings)
		})
	}
	if err := swap(ctx); err != nil {
		return err
	}

	slog.InfoContext(ctx, "wiki: vectorize article done",
		"article_id", id, "chunks", len(chunkTexts), "version", pubVersion)
	return nil
}

// embedWithModel 生成向量并返回生成它们的模型名，二者来自同一客户端快照。
// 若 embedder 支持 EmbedderWithModel（*SwappableClient / *Client）则一次调用完成；
// 否则退化为"先读模型名再 Embed"（旧 mock 兼容，存在热切换不一致窗口，仅测试路径）。
func (h *VectorizeHandler) embedWithModel(
	ctx context.Context, texts []string,
) (embeddings [][]float32, model string, err error) {
	if em, ok := h.embed.(llm.EmbedderWithModel); ok {
		return em.EmbedWithModel(ctx, texts)
	}
	embeddings, err = h.embed.Embed(ctx, texts)
	if err != nil {
		return nil, h.embeddingModel(), err
	}
	return embeddings, h.embeddingModel(), nil
}

// embeddingModel 返回当前生效的向量模型名；实现未暴露时返回空串（视为未知，检索侧不过滤）。
func (h *VectorizeHandler) embeddingModel() string {
	if p, ok := h.embed.(llm.EmbeddingModelNamer); ok {
		return p.EmbeddingModel()
	}
	return ""
}

// clearChunks 纯图片/空正文版本重建：版本复核后事务性清除旧切片（不写入新切片）。
// 与 swapChunks 相同的版本守卫，避免 embedding 期间文章被再次更新后误清白更新的切片。
func (h *VectorizeHandler) clearChunks(ctx context.Context, id int64, version int) error {
	doClear := func(ctx context.Context) error {
		return h.clearChunksTx(ctx, id, version)
	}
	if h.tx == nil {
		return doClear(ctx)
	}
	return h.tx.WithTx(ctx, doClear)
}

// clearChunksTx 在调用方事务内完成版本复核 + 旧切片清除。
func (h *VectorizeHandler) clearChunksTx(ctx context.Context, id int64, version int) error {
	cur, err := h.articles.LockPublishedVersion(ctx, id)
	if err != nil {
		return fmt.Errorf("lock article %d published version: %w", id, err)
	}
	if cur != version {
		slog.WarnContext(ctx, "wiki: article published version changed during embedding, skip clearing chunks",
			"article_id", id, "embedded_version", version, "current_published_version", cur)
		return fmt.Errorf("article %d published version changed %d -> %d during embedding: %w",
			id, version, cur, asynqlib.SkipRetry)
	}
	if _, err := h.chunks.DeactivateByArticle(ctx, id); err != nil {
		return fmt.Errorf("deactivate chunks for article %d: %w", id, err)
	}
	if _, err := h.chunks.DeleteInactiveByArticle(ctx, id); err != nil {
		return fmt.Errorf("delete inactive chunks for article %d: %w", id, err)
	}
	return nil
}

// swapChunks 在调用方事务内完成版本复核 + 切片原子替换。
// 版本已变化（embedding 期间文章被重新审核发布）时放弃本次写入：新版本自有其入队任务负责重建，
// 本任务返回 SkipRetry 避免重复覆盖（重试也仍会被版本复核拦下）。
// 复核的是 published_version：纯元数据变更（封面/来源）只递增 version，不影响已发布快照。
func (h *VectorizeHandler) swapChunks(
	ctx context.Context, id int64, version int, model string, chunkTexts []string, embeddings [][]float32,
) error {
	cur, err := h.articles.LockPublishedVersion(ctx, id)
	if err != nil {
		return fmt.Errorf("lock article %d published version: %w", id, err)
	}
	if cur != version {
		slog.WarnContext(ctx, "wiki: article published version changed during embedding, discard stale chunks",
			"article_id", id, "embedded_version", version, "current_published_version", cur)
		return fmt.Errorf("article %d published version changed %d -> %d during embedding: %w",
			id, version, cur, asynqlib.SkipRetry)
	}
	// 先失效旧切片再写入新切片（与 Update 路径协同：Update 已先 DeactivateByArticle，
	// 这里二次调用幂等；Approve 路径首次写入时无旧切片，RowsAffected=0）。
	if _, err := h.chunks.DeactivateByArticle(ctx, id); err != nil {
		return fmt.Errorf("deactivate chunks for article %d: %w", id, err)
	}
	// 物理删除已失效切片：避免旧版本切片无限堆积（数据卫生）。
	if _, err := h.chunks.DeleteInactiveByArticle(ctx, id); err != nil {
		return fmt.Errorf("delete inactive chunks for article %d: %w", id, err)
	}
	// 记录向量所属模型：模型切换后据此识别需重建的切片（检索侧按模型过滤，避免新旧向量混用）。
	// model 为任务开始时的快照，与 embeddings 实际使用的模型一致（P1）。
	for i, text := range chunkTexts {
		chunk := &entity.ArticleChunk{
			ArticleID:      id,
			ChunkIndex:     i,
			Content:        text,
			ContentHash:    contenthash.SHA256(text),
			Embedding:      pgvector.NewVector(embeddings[i]),
			EmbeddingModel: model,
			IsActive:       true,
			Version:        version,
		}
		if err := h.chunks.Create(ctx, chunk); err != nil {
			return fmt.Errorf("create chunk[%d] for article %d: %w", i, id, err)
		}
	}
	return nil
}

// reHTMLBlockTags 匹配块级标签（含 <br>），转纯文本时替换为空格以保留语义边界，
// 否则 "<p>甲</p><p>乙</p>" 会被拼成 "甲乙"，跨块语义粘连。
var reHTMLBlockTags = regexp.MustCompile(
	`(?i)</?(?:p|div|br|hr|li|ul|ol|h[1-6]|blockquote|pre|table|thead|tbody|tr|td|th|section|article)[^>]*>`)

// reHTMLTags 匹配剩余任意标签（含 <img src="...">）：直接丢弃，不参与向量化。
var reHTMLTags = regexp.MustCompile(`<[^>]*>`)

// reHTMLHeading 匹配 h1-h6 并捕获其内容：标题语义用于分节切片，不能当普通块级标签丢弃。
var reHTMLHeading = regexp.MustCompile(`(?is)<h[1-6][^>]*>(.*?)</h[1-6]>`)

// htmlToPlainText 将富文本 HTML 正文转为纯文本，供切片向量化使用。
// 步骤：标题→"## 标题" 独立行 → 块级标签→空格 → 其余标签（含 img）丢弃 → 解码 HTML 实体 → 逐行空白归一。
// 保留标题行的换行结构，供 chunkContent 分节；&nbsp; 解码出的 NBSP 也会被归一掉。
func htmlToPlainText(s string) string {
	if !strings.Contains(s, "<") && !strings.Contains(s, "&") {
		// 已是纯文本：原样保留（含 "## " 标题行），避免无谓的复制开销。
		return strings.TrimSpace(s)
	}
	s = reHTMLHeading.ReplaceAllStringFunc(s, func(m string) string {
		sub := reHTMLHeading.FindStringSubmatch(m)
		title := collapseSpaces(reHTMLTags.ReplaceAllString(reHTMLBlockTags.ReplaceAllString(sub[1], " "), ""))
		if title == "" {
			return "\n"
		}
		return "\n## " + title + "\n"
	})
	s = reHTMLBlockTags.ReplaceAllString(s, " ")
	s = reHTMLTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = collapseSpaces(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// collapseSpaces 把连续空白（含 NBSP）归一为单个空格并去首尾空白。
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// contentSection 切片分节结果：标题为空表示首个 "## " 之前的无标题正文。
type contentSection struct {
	title string
	body  string
}

// chunkContent 按标题与句子边界切分 content（content 应为纯文本，见 htmlToPlainText）。
// 规则：
//  1. 以行首 "## " 分节；节内按段落（空行）与中文句读（。！？；!?;）找切分点，累积到 size runes；
//  2. 只有单句超过 size 时才硬切；
//  3. 章节非空时，每个切片正文前加一行 "【章节：{标题}】"（不占 size 预算）；
//  4. 相邻切片保留上一片末尾最多 overlap runes，优先整句，避免把整句重复到不可读。
func chunkContent(content string, size, overlap int) []string {
	if size <= 0 || strings.TrimSpace(content) == "" {
		return nil
	}
	if overlap < 0 || overlap >= size {
		overlap = 0
	}
	var out []string
	for _, sec := range splitSections(content) {
		out = append(out, packSection(sec, size, overlap)...)
	}
	return out
}

// splitSections 按行首 "## " 把纯文本分成若干节；空节（无标题且无正文）直接丢弃。
func splitSections(content string) []contentSection {
	var sections []contentSection
	var cur contentSection
	var lines []string
	flush := func() {
		body := joinParagraphs(lines)
		if cur.title != "" || body != "" {
			sections = append(sections, contentSection{title: cur.title, body: body})
		}
		lines = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			cur = contentSection{title: strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))}
			continue
		}
		lines = append(lines, line)
	}
	flush()
	return sections
}

// joinParagraphs 把非空行按段合并（单换行视为折行），空行分段，段间用单个换行连接。
func joinParagraphs(lines []string) string {
	var paras []string
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			paras = append(paras, strings.Join(buf, " "))
			buf = nil
		}
	}
	for _, line := range lines {
		line = collapseSpaces(line)
		if line == "" {
			flush()
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return strings.Join(paras, "\n")
}

// packSection 在单节内按句子/段落边界贪心累积切片，并给每片加章节前缀。
func packSection(sec contentSection, size, overlap int) []string {
	runes := []rune(sec.body)
	if len(runes) == 0 {
		return nil
	}
	bounds := sentenceBounds(sec.body)
	head := ""
	if sec.title != "" {
		head = "【章节：" + sec.title + "】\n"
	}
	var out []string
	prev := ""
	for pos := 0; pos < len(runes); {
		prefix := overlapSuffix(prev, overlap)
		budget := size - len([]rune(prefix))
		end := pos + budget
		if end > len(runes) {
			end = len(runes)
		}
		if b := lastBoundary(bounds, pos, end); b > pos {
			end = b
		}
		piece := strings.TrimSpace(string(runes[pos:end]))
		pos = end
		if piece == "" {
			continue
		}
		out = append(out, head+prefix+piece)
		prev = piece
	}
	return out
}

// sentenceBounds 返回 body（段落以单个换行分隔）中可安全切分的 rune 偏移：
// 句读之后与段落末尾，按升序排列。
func sentenceBounds(body string) []int {
	paras := strings.Split(body, "\n")
	var bounds []int
	off := 0
	for pi, para := range paras {
		rs := []rune(para)
		for i, r := range rs {
			if i < len(rs)-1 && isSentenceEnd(r) {
				bounds = append(bounds, off+i+1)
			}
		}
		off += len(rs)
		bounds = append(bounds, off)
		if pi < len(paras)-1 {
			off++ // 段间换行
		}
	}
	return bounds
}

// lastBoundary 返回 (from, to] 内最大的切分点；不存在时返回 -1。
func lastBoundary(bounds []int, from, to int) int {
	best := -1
	for _, b := range bounds {
		if b <= from {
			continue
		}
		if b > to {
			break
		}
		best = b
	}
	return best
}

// overlapSuffix 取上一片末尾最多 overlap 个 rune；优先从整句句首开始，
// 避免为了重叠把上一片的长句整句重复进下一片。
func overlapSuffix(prev string, overlap int) string {
	if overlap <= 0 || prev == "" {
		return ""
	}
	rs := []rune(prev)
	if len(rs) <= overlap {
		return prev
	}
	for start := len(rs) - overlap; start < len(rs); start++ {
		if isSentenceEnd(rs[start-1]) || rs[start-1] == '\n' {
			return string(rs[start:])
		}
	}
	return string(rs[len(rs)-overlap:])
}

// isSentenceEnd 判断是否为中文/英文句读结束符。
func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '!', '?', ';':
		return true
	}
	return false
}
