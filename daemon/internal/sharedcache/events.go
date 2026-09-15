package sharedcache

// EventLogger records shared-cache activity as structured events so operators
// can tell whether clients actually fetch from and upload to the central node.
// *logger.Logger satisfies it; a nil EventLogger disables logging.
type EventLogger interface {
	Event(kind string, fields map[string]any)
}

type nopLogger struct{}

func (nopLogger) Event(string, map[string]any) {}

func loggerOrNop(lg EventLogger) EventLogger {
	if lg == nil {
		return nopLogger{}
	}
	return lg
}
