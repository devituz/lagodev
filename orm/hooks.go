package orm

import (
	"context"

	"github.com/devituz/lagodev/database"
)

// HookContext is passed to lifecycle hooks. It exposes the active
// transaction-or-connection executor and the underlying context.Context.
type HookContext struct {
	Ctx  context.Context
	Conn *database.Connection
	Tx   *database.Tx
}

// Executor returns the active executor, preferring the transaction if open.
func (h *HookContext) Executor() database.Executor {
	if h.Tx != nil {
		return h.Tx
	}
	return h.Conn
}

func dispatchHook(model any, fn string, ctx *HookContext) error {
	switch fn {
	case "BeforeCreate":
		if h, ok := model.(BeforeCreateHook); ok {
			return h.BeforeCreate(ctx)
		}
	case "AfterCreate":
		if h, ok := model.(AfterCreateHook); ok {
			return h.AfterCreate(ctx)
		}
	case "BeforeUpdate":
		if h, ok := model.(BeforeUpdateHook); ok {
			return h.BeforeUpdate(ctx)
		}
	case "AfterUpdate":
		if h, ok := model.(AfterUpdateHook); ok {
			return h.AfterUpdate(ctx)
		}
	case "BeforeSave":
		if h, ok := model.(BeforeSaveHook); ok {
			return h.BeforeSave(ctx)
		}
	case "AfterSave":
		if h, ok := model.(AfterSaveHook); ok {
			return h.AfterSave(ctx)
		}
	case "BeforeDelete":
		if h, ok := model.(BeforeDeleteHook); ok {
			return h.BeforeDelete(ctx)
		}
	case "AfterDelete":
		if h, ok := model.(AfterDeleteHook); ok {
			return h.AfterDelete(ctx)
		}
	case "AfterFind":
		if h, ok := model.(AfterFindHook); ok {
			return h.AfterFind(ctx)
		}
	}
	return nil
}
