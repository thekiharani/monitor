package app

import "github.com/thekiharani/monitor/internal/cloud/logging"

func (a *App) logError(message, component, operation string, err error, attrs ...any) {
	attrs = append([]any{"component", component, "operation", operation}, attrs...)
	logging.Error(a.log, message, err, attrs...)
}

func (a *App) logWarn(message, component, operation string, err error, attrs ...any) {
	attrs = append([]any{"component", component, "operation", operation}, attrs...)
	logging.Warn(a.log, message, err, attrs...)
}

func (a *App) logEvent(message, component, operation string, attrs ...any) {
	a.log.Info(message, append([]any{"component", component, "operation", operation}, attrs...)...)
}
