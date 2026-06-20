package repository

import (
	"context"
	"errors"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
)

type MemberRepository struct {
	db *gorm.DB
}

func NewMemberRepository(db *gorm.DB) *MemberRepository {
	return &MemberRepository{db: db}
}

func (r *MemberRepository) SetMemoryConsent(ctx context.Context, guildID, userID string, consent bool) (store.GuildMember, error) {
	now := time.Now().UTC()
	var member store.GuildMember
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("guild_id = ? AND user_id = ?", guildID, userID).First(&member).Error
		if err == nil {
			if err := tx.Model(&member).Updates(map[string]any{
				"memory_consent": consent,
				"updated_at":     now,
			}).Error; err != nil {
				return err
			}
			return tx.First(&member, member.ID).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		member = store.GuildMember{
			GuildID:          guildID,
			UserID:           userID,
			MemoryConsent:    consent,
			AssistantAllowed: true,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		return tx.Create(&member).Error
	})
	return member, err
}

func (r *MemberRepository) MemoryConsent(ctx context.Context, guildID, userID string) (bool, error) {
	var member store.GuildMember
	err := r.db.WithContext(ctx).Where("guild_id = ? AND user_id = ?", guildID, userID).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return member.MemoryConsent, nil
}
