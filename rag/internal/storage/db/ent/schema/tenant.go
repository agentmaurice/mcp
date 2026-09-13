package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/rs/xid"
)

// Tenant holds the schema definition for the Tenant entity
type Tenant struct {
	ent.Schema
}

// Fields of the Tenant
func (Tenant) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			GoType(xid.ID{}).
			DefaultFunc(xid.New),
		field.String("deployment_id").GoType(xid.ID{}).Optional().Nillable(),
		field.String("name").NotEmpty(),
		field.String("api_key_hash").Sensitive(),
		field.Bool("is_default").Default(false),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Tenant
func (Tenant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("documents", Document.Type),
		edge.To("ingest_jobs", IngestJob.Type),
	}
}

// Indexes of the Tenant
func (Tenant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("deployment_id", "name").Unique(),
		index.Fields("deployment_id", "is_default"),
	}
}
