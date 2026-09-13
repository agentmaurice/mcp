package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/rs/xid"
)

// IngestJob holds the schema definition for the IngestJob entity
type IngestJob struct {
	ent.Schema
}

// Fields of the IngestJob
func (IngestJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			GoType(xid.ID{}).
			DefaultFunc(xid.New),
		field.String("tenant_id").GoType(xid.ID{}),
		field.Enum("status").
			Values("pending", "running", "failed", "completed").
			Default("pending"),
		field.String("source_type").NotEmpty(), // url, text, file
		field.JSON("source_payload", map[string]interface{}{}),
		field.String("message").Optional(),
		field.Int("progress").Default(0), // 0-100
		field.Bool("has_content_findings").Default(false),
		field.JSON("content_findings", map[string]interface{}{}).Optional(),
		field.String("content_detection_profile").Default("none"),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the IngestJob
func (IngestJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tenant", Tenant.Type).
			Ref("ingest_jobs").
			Field("tenant_id").
			Required().
			Unique(),
	}
}
