package service

import (
	"context"
	"errors"

	"switchctl/internal/store"
)

// GetOperation 按 ID 查询操作。
func (s *Service) GetOperation(ctx context.Context, id string) (*store.Operation, error) {
	op, err := s.store.GetOperation(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound("操作不存在: " + id)
	}
	return op, err
}

// ListOperations 查询操作列表。
func (s *Service) ListOperations(ctx context.Context, f store.OperationFilter) ([]*store.Operation, error) {
	return s.store.ListOperations(ctx, f)
}
