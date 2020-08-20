package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jinzhu/gorm"
)

const trackScopeKey = "gorm_tracer"

var writer *bufio.Writer

func getWriter() *bufio.Writer {
	if writer == nil {
		f, err := os.Create(fmt.Sprintf("gorm.%d.log", time.Now().UnixNano()))
		if err != nil {
			return nil
		}
		writer = bufio.NewWriter(f)
	}
	return writer
}

func writeEntry(gormEvent *GormEvent) {
	bs, err := json.Marshal(gormEvent)
	if err != nil {
		return
	}
	w := getWriter()
	w.Write(bs)
	w.WriteByte('\n')
	w.Flush()
}

type GormEvent struct {
	StartTime    time.Time              `json:"start_time"`
	Query        string                 `json:"query"`
	EndTime      time.Time              `json:"end_time"`
	EventType    string                 `json:"event_type"`
	RowsAffected int64                  `json:"rows_affected"`
	Errors       []error                `json:"errors"`
	InstanceID   string                 `json:"db_instance_id"`
	IsComplete   bool                   `json:"completed"`
	Vars         map[string]interface{} `json:"settings"`
}

type tracer struct {
	ID     string
	Events map[string]*GormEvent
	mu     *sync.Mutex
}

func TraceDB(db *gorm.DB) (*gorm.DB, func()) {
	t := tracer{
		Events: make(map[string]*GormEvent),
		mu:     &sync.Mutex{},
	}

	// Create
	db.Callback().Create().After("gorm:begin_transaction").Register(trackScopeKey, t.CreateEvent)
	db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register(trackScopeKey+":complete", t.CompleteCreateEvent)

	// RowQuery
	db.Callback().RowQuery().Before("gorm:row_query").Register(trackScopeKey, t.RowQueryEvent)
	db.Callback().RowQuery().After("gorm:row_query").Register(trackScopeKey+":complete", t.CompleteRowQueryEvent)

	// Query
	db.Callback().Query().Before("gorm:query").Register(trackScopeKey, t.QueryEvent)
	db.Callback().Query().After("gorm:after_query").Register(trackScopeKey+":complete", t.CompleteQueryEvent)

	// Update
	db.Callback().Update().After("gorm:begin_transaction").Register(trackScopeKey, t.UpdateEvent)
	db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(trackScopeKey+":complete", t.CompleteUpdateEvent)

	// Delete
	db.Callback().Delete().After("gorm:begin_transaction").Register(trackScopeKey, t.DeleteEvent)
	db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register(trackScopeKey+":complete", t.CompleteDeleteEvent)

	return db, func() {
		t.Close()
	}
}

func (t *tracer) CreateEvent(scope *gorm.Scope){
	t.AddEvent("create", scope)
}

func (t *tracer) QueryEvent(scope *gorm.Scope){
	t.AddEvent("query", scope)
}

func (t *tracer) RowQueryEvent(scope *gorm.Scope){
	t.AddEvent("row_query", scope)
}

func (t *tracer) UpdateEvent(scope *gorm.Scope){
	t.AddEvent("update", scope)
}

func (t *tracer) DeleteEvent(scope *gorm.Scope){
	t.AddEvent("delete", scope)
}

func (t *tracer) CompleteCreateEvent(scope *gorm.Scope){
	t.CompleteEvent(scope)
}

func (t *tracer) CompleteQueryEvent(scope *gorm.Scope){
	t.CompleteEvent(scope)
}

func (t *tracer) CompleteRowQueryEvent(scope *gorm.Scope){
	t.CompleteEvent(scope)
}

func (t *tracer) CompleteUpdateEvent(scope *gorm.Scope){
	t.CompleteEvent(scope)
}

func (t *tracer) CompleteDeleteEvent(scope *gorm.Scope){
	t.CompleteEvent(scope)
}

func (t *tracer) EventGenerator(eventType string) func(scope *gorm.Scope) {
	return func(scope *gorm.Scope) {
		t.AddEvent(eventType, scope)
	}
}

func (t *tracer) AddEvent(eventType string, scope *gorm.Scope) {
	key := uuid.New().String()
	scope.Set(trackScopeKey, key)
	e := &GormEvent{
		StartTime:  time.Now(),
		EventType:  eventType,
		InstanceID: scope.InstanceID(),
	}
	extractFromScope(e, scope)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.Events[key] = e
}

func (t *tracer) CompleteEvent(scope *gorm.Scope) {
	key, ok := scope.Get(trackScopeKey)
	if !ok {
		return
	}

	entry := t.Events[key.(string)]
	entry.EndTime = time.Now()
	entry.IsComplete = true
	extractFromScope(entry, scope)
	writeEntry(entry)
}

var knownAttrs = []string{
	"gorm:insert_option",
	"gorm:query_option",
	"gorm:delete_option",
	"gorm:started_transaction",
	"gorm:table_options",
}

func copyScopeAttrs(scope *gorm.Scope) map[string]interface{} {
	attrs := make(map[string]interface{})
	for _, a := range knownAttrs {
		if v, ok := scope.Get(a); ok {
			attrs[a] = v
		}
	}
	return attrs
}

func extractFromScope(entry *GormEvent, scope *gorm.Scope) {
	entry.Query = scope.SQL
	entry.RowsAffected = scope.DB().RowsAffected
	entry.Errors = scope.DB().GetErrors()
	entry.Vars = copyScopeAttrs(scope)
}

func (t *tracer) Close() {
	for _, e := range t.Events {
		if !e.IsComplete {
			e.EndTime = time.Now()
			writeEntry(e)
		}
	}
}
