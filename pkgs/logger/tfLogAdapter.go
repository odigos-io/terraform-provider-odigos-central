package logger

import (
	"context"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

type TfLogAdapter struct{}

func (l *TfLogAdapter) Info(ctx context.Context, msg string, args ...any) {
	tflog.Info(ctx, msg, argsToMap(args...))
}

func (l *TfLogAdapter) Debug(ctx context.Context, msg string, args ...any) {
	tflog.Debug(ctx, msg, argsToMap(args...))
}

func (l *TfLogAdapter) Error(ctx context.Context, msg string, args ...any) {
	tflog.Error(ctx, msg, argsToMap(args...))
}

// Helper to convert slog-style args to map[string]interface{}
func argsToMap(args ...any) map[string]any {
	m := make(map[string]any)
	for i := 0; i < len(args)-1; i += 2 {
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		m[key] = args[i+1]
	}
	return m
}
