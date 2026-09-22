package voucher

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// VoucherRecord 表示本地持久化的单张券码状态。
type VoucherRecord struct {
	Code       string     `json:"code"`
	GrantID    int64      `json:"grant_id,omitempty"`
	UID        string     `json:"uid"`
	Nickname   string     `json:"nickname"`
	PrizeName  string     `json:"prize_name"`
	IsUsed     bool       `json:"is_used"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	Notified   bool       `json:"notified"`
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
	ValidTo    string     `json:"valid_to,omitempty"`
}

// EnrichedVoucher 在上游券码基础上附加本地使用状态。
type EnrichedVoucher struct {
	upstream.SchoolVoucher
	IsUsed bool       `json:"is_used"`
	UsedAt *time.Time `json:"used_at,omitempty"`
}

// StoreData 对应 vouchers.json 的完整结构。
type StoreData struct {
	Version  int                       `json:"version"`
	Vouchers map[string]*VoucherRecord `json:"vouchers"`
}

// Store 负责券码本地状态持久化及并发安全访问。
type Store struct {
	filePath string
	mu       sync.RWMutex
	data     StoreData
}

// NewStore 创建或从本地文件加载券码存储。
func NewStore(filePath string) (*Store, error) {
	s := &Store{
		filePath: filePath,
		data: StoreData{
			Version:  1,
			Vouchers: make(map[string]*VoucherRecord),
		},
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if dir := filepath.Dir(filePath); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0755); err != nil {
					return nil, err
				}
			}
			return s, nil
		}
		return nil, err
	}

	if len(content) > 0 {
		if err := json.Unmarshal(content, &s.data); err != nil {
			return nil, err
		}
	}

	if s.data.Vouchers == nil {
		s.data.Vouchers = make(map[string]*VoucherRecord)
	}
	if s.data.Version == 0 {
		s.data.Version = 1
	}

	return s, nil
}

// saveAtomicLocked 将数据原子写入文件（必须在持有写锁时调用）。
func (s *Store) saveAtomicLocked() error {
	if dir := filepath.Dir(s.filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, b, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.filePath)
}

// IsUsed 查询券码是否标记为已使用。
func (s *Store) IsUsed(code string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.data.Vouchers[code]
	return ok && rec.IsUsed
}

// SetUsed 更新券码的使用状态并原子持久化。
func (s *Store) SetUsed(code string, isUsed bool, uid, nickname, prizeName, validTo string) error {
	if s == nil {
		return errors.New("voucher store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.data.Vouchers[code]
	if !ok {
		rec = &VoucherRecord{
			Code:      code,
			UID:       uid,
			Nickname:  nickname,
			PrizeName: prizeName,
			ValidTo:   validTo,
		}
		s.data.Vouchers[code] = rec
	} else {
		if rec.UID == "" && uid != "" {
			rec.UID = uid
		}
		if rec.Nickname == "" && nickname != "" {
			rec.Nickname = nickname
		}
		if rec.PrizeName == "" && prizeName != "" {
			rec.PrizeName = prizeName
		}
		if rec.ValidTo == "" && validTo != "" {
			rec.ValidTo = validTo
		}
	}

	rec.IsUsed = isUsed
	if isUsed {
		now := time.Now()
		rec.UsedAt = &now
	} else {
		rec.UsedAt = nil
	}

	return s.saveAtomicLocked()
}

// FilterUnnotified 过滤出未通知过的券码列表。
func (s *Store) FilterUnnotified(vouchers []upstream.SchoolVoucher) []upstream.SchoolVoucher {
	if s == nil {
		return vouchers
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]upstream.SchoolVoucher, 0, len(vouchers))
	for _, v := range vouchers {
		rec, ok := s.data.Vouchers[v.Code]
		if !ok || !rec.Notified {
			result = append(result, v)
		}
	}
	return result
}

// MarkNotified 批量标记券码为已通知并持久化。
func (s *Store) MarkNotified(uid, nickname string, vouchers []upstream.SchoolVoucher) error {
	if s == nil {
		return nil
	}
	if len(vouchers) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, v := range vouchers {
		rec, ok := s.data.Vouchers[v.Code]
		if !ok {
			rec = &VoucherRecord{
				Code:      v.Code,
				GrantID:   v.GrantID,
				UID:       uid,
				Nickname:  nickname,
				PrizeName: v.PrizeName,
				ValidTo:   v.ValidTo,
			}
			s.data.Vouchers[v.Code] = rec
		} else {
			if rec.UID == "" && uid != "" {
				rec.UID = uid
			}
			if rec.Nickname == "" && nickname != "" {
				rec.Nickname = nickname
			}
			if rec.PrizeName == "" && v.PrizeName != "" {
				rec.PrizeName = v.PrizeName
			}
			if rec.ValidTo == "" && v.ValidTo != "" {
				rec.ValidTo = v.ValidTo
			}
			if rec.GrantID == 0 && v.GrantID != 0 {
				rec.GrantID = v.GrantID
			}
		}
		rec.Notified = true
		rec.NotifiedAt = &now
	}

	return s.saveAtomicLocked()
}

// Enrich 为上游券码附加本地使用状态。
func (s *Store) Enrich(vouchers []upstream.SchoolVoucher) []EnrichedVoucher {
	if s == nil {
		result := make([]EnrichedVoucher, len(vouchers))
		for i, v := range vouchers {
			result[i] = EnrichedVoucher{SchoolVoucher: v}
		}
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]EnrichedVoucher, len(vouchers))
	for i, v := range vouchers {
		ev := EnrichedVoucher{
			SchoolVoucher: v,
		}
		if rec, ok := s.data.Vouchers[v.Code]; ok {
			ev.IsUsed = rec.IsUsed
			if rec.UsedAt != nil {
				t := *rec.UsedAt
				ev.UsedAt = &t
			}
		}
		result[i] = ev
	}
	return result
}
