package repository

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/textutil"
	"gorm.io/gorm"
)

const maxKnowledgeChunkBytes = 1200

var ftsTermPattern = regexp.MustCompile(`[A-Za-z0-9_]+`)

var searchStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {}, "do": {}, "for": {}, "from": {},
	"how": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {}, "what": {}, "when": {},
	"where": {}, "who": {}, "why": {}, "with": {},
}

type KnowledgeRepository struct {
	db *gorm.DB
}

type KnowledgeSearchResult struct {
	ChunkID    uint
	DocumentID uint
	Title      string
	Snippet    string
	Content    string
}

func NewKnowledgeRepository(db *gorm.DB) *KnowledgeRepository {
	return &KnowledgeRepository{db: db}
}

func (r *KnowledgeRepository) AddDocument(ctx context.Context, document store.KnowledgeDocument, content string) (store.KnowledgeDocument, error) {
	now := time.Now().UTC()
	document.CreatedAt = firstTime(document.CreatedAt, now)
	document.UpdatedAt = firstTime(document.UpdatedAt, now)
	document.Source = firstNonEmpty(document.Source, "admin")
	document.SourceMetadata = firstNonEmpty(document.SourceMetadata, "{}")
	if document.Confidence <= 0 {
		document.Confidence = 1
	}
	document.Enabled = true

	chunks := splitKnowledgeChunks(content)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&document).Error; err != nil {
			return err
		}
		for i, chunkContent := range chunks {
			chunk := store.KnowledgeChunk{
				DocumentID:  document.ID,
				GuildID:     document.GuildID,
				Ordinal:     i,
				Content:     chunkContent,
				ContentHash: contentHash(chunkContent),
				CreatedAt:   now,
			}
			if err := tx.Create(&chunk).Error; err != nil {
				return err
			}
			if err := tx.Exec(
				`INSERT INTO knowledge_fts(rowid, guild_id, document_id, chunk_id, title, content) VALUES (?, ?, ?, ?, ?, ?)`,
				chunk.ID,
				document.GuildID,
				document.ID,
				chunk.ID,
				document.Title,
				chunk.Content,
			).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return document, err
}

func (r *KnowledgeRepository) Search(ctx context.Context, guildID, query string, limit int) ([]KnowledgeSearchResult, error) {
	match := sanitizeFTSQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	if ok, err := r.usesFTS5(ctx); err != nil {
		return nil, err
	} else if !ok {
		return r.searchFallback(ctx, guildID, query, limit)
	}

	var rows []struct {
		ChunkID    uint
		DocumentID uint
		Title      string
		Snippet    string
		Content    string
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT
			CAST(chunk_id AS INTEGER) AS chunk_id,
			CAST(document_id AS INTEGER) AS document_id,
			title,
			snippet(knowledge_fts, 4, '[', ']', '...', 18) AS snippet,
			content
		FROM knowledge_fts
		WHERE knowledge_fts MATCH ? AND guild_id = ?
		ORDER BY bm25(knowledge_fts)
		LIMIT ?
	`, match, guildID, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	results := make([]KnowledgeSearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, KnowledgeSearchResult{
			ChunkID:    row.ChunkID,
			DocumentID: row.DocumentID,
			Title:      row.Title,
			Snippet:    row.Snippet,
			Content:    row.Content,
		})
	}
	return results, nil
}

func (r *KnowledgeRepository) ChunksByDocument(ctx context.Context, guildID string, documentID uint) ([]store.KnowledgeChunk, error) {
	var chunks []store.KnowledgeChunk
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND document_id = ?", guildID, documentID).
		Order("ordinal ASC").
		Find(&chunks).Error
	return chunks, err
}

func (r *KnowledgeRepository) HasContentHash(ctx context.Context, guildID, hash string) (bool, error) {
	if strings.TrimSpace(hash) == "" {
		return false, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&store.KnowledgeChunk{}).
		Where("guild_id = ? AND content_hash = ?", guildID, hash).
		Limit(1).
		Count(&count).Error
	return count > 0, err
}

func (r *KnowledgeRepository) DisableExpired(ctx context.Context, now time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Model(&store.KnowledgeDocument{}).
		Where("enabled = ? AND expires_at IS NOT NULL AND expires_at <= ?", true, now.UTC()).
		Updates(map[string]any{"enabled": false, "updated_at": now.UTC()})
	return result.RowsAffected, result.Error
}

func (r *KnowledgeRepository) AddEmbeddings(ctx context.Context, embeddings []store.KnowledgeEmbedding) error {
	if len(embeddings) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, embedding := range embeddings {
			if err := tx.Exec(`
				INSERT INTO knowledge_embeddings(chunk_id, model, vector, created_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(chunk_id, model) DO UPDATE SET
					vector = excluded.vector,
					created_at = excluded.created_at
			`, embedding.ChunkID, embedding.Model, embedding.Vector, embedding.CreatedAt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *KnowledgeRepository) searchFallback(ctx context.Context, guildID, query string, limit int) ([]KnowledgeSearchResult, error) {
	terms := searchTerms(query, 3)
	if len(terms) == 0 {
		return nil, nil
	}

	var conditions []string
	var args []any
	args = append(args, guildID)
	for _, term := range terms {
		conditions = append(conditions, "(lower(title) LIKE ? OR lower(content) LIKE ?)")
		like := "%" + term + "%"
		args = append(args, like, like)
	}
	args = append(args, limit)

	var rows []struct {
		ChunkID    uint
		DocumentID uint
		Title      string
		Content    string
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT
			CAST(chunk_id AS INTEGER) AS chunk_id,
			CAST(document_id AS INTEGER) AS document_id,
			title,
			content
		FROM knowledge_fts
		WHERE guild_id = ? AND `+strings.Join(conditions, " AND ")+`
		ORDER BY document_id ASC, chunk_id ASC
		LIMIT ?
	`, args...).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	results := make([]KnowledgeSearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, KnowledgeSearchResult{
			ChunkID:    row.ChunkID,
			DocumentID: row.DocumentID,
			Title:      row.Title,
			Snippet:    fallbackSnippet(row.Content, terms[0]),
			Content:    row.Content,
		})
	}
	return results, nil
}

func (r *KnowledgeRepository) usesFTS5(ctx context.Context) (bool, error) {
	var createSQL string
	err := r.db.WithContext(ctx).Raw(`SELECT sql FROM sqlite_master WHERE name = 'knowledge_fts'`).Scan(&createSQL).Error
	if err != nil {
		return false, err
	}
	return strings.Contains(strings.ToLower(createSQL), "virtual table"), nil
}

func (r *KnowledgeRepository) DeleteDocument(ctx context.Context, guildID string, documentID uint) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM knowledge_fts WHERE guild_id = ? AND document_id = ?`, guildID, documentID).Error; err != nil {
			return err
		}
		result := tx.Where("guild_id = ? AND id = ?", guildID, documentID).Delete(&store.KnowledgeDocument{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r *KnowledgeRepository) ListDocuments(ctx context.Context, guildID string, limit int) ([]store.KnowledgeDocument, error) {
	if limit <= 0 || limit > 50 {
		limit = 25
	}
	var documents []store.KnowledgeDocument
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", guildID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&documents).Error
	return documents, err
}

func splitKnowledgeChunks(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return []string{"empty document"}
	}

	var chunks []string
	for len(content) > maxKnowledgeChunkBytes {
		prefix := textutil.PrefixBytes(content, maxKnowledgeChunkBytes)
		splitAt := strings.LastIndex(prefix, "\n\n")
		if splitAt < maxKnowledgeChunkBytes/3 {
			splitAt = strings.LastIndex(prefix, ". ")
		}
		if splitAt < maxKnowledgeChunkBytes/3 {
			splitAt = len(prefix)
		}

		chunks = append(chunks, strings.TrimSpace(content[:splitAt]))
		content = strings.TrimSpace(content[splitAt:])
	}
	if content != "" {
		chunks = append(chunks, content)
	}
	return chunks
}

func sanitizeFTSQuery(query string) string {
	terms := searchTerms(query, 8)
	if len(terms) == 0 {
		return ""
	}

	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " ")
}

func searchTerms(query string, limit int) []string {
	rawTerms := ftsTermPattern.FindAllString(strings.ToLower(query), -1)
	terms := make([]string, 0, len(rawTerms))
	for _, term := range rawTerms {
		if _, skip := searchStopWords[term]; skip {
			continue
		}
		terms = append(terms, term)
		if limit > 0 && len(terms) == limit {
			break
		}
	}
	return terms
}

func firstTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value
}

func fallbackSnippet(content, term string) string {
	content = strings.TrimSpace(content)
	if len(content) <= 180 {
		return content
	}
	index := strings.Index(strings.ToLower(content), strings.ToLower(term))
	if index < 0 {
		return textutil.Truncate(content, 180, "...")
	}
	start := index - 60
	if start < 0 {
		start = 0
	}
	end := start + 180
	if end > len(content) {
		end = len(content)
	}
	prefix := ""
	if start > 0 {
		prefix = "..."
	}
	suffix := ""
	if end < len(content) {
		suffix = "..."
	}
	return prefix + strings.TrimSpace(textutil.SliceBytes(content, start, end)) + suffix
}
