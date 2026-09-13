package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/rs/xid"
)

// Document holds the schema definition for the Document entity
type Document struct {
	ent.Schema
}

// Fields of the Document
func (Document) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			GoType(xid.ID{}).
			DefaultFunc(xid.New),
		field.String("tenant_id").GoType(xid.ID{}),
		field.String("title"),
		field.String("source_url").Optional(),
		field.JSON("metadata", map[string]interface{}{}).Optional(),
		field.String("normalized_hash").Optional().Nillable(),
		field.String("doc_type").Default("generic"),
		field.String("content_type").Optional().Nillable(),
		field.Int64("size").Optional().Nillable(),
		field.String("duplicate_of").GoType(xid.ID{}).Optional().Nillable(),
		field.Bool("has_pii").Default(false),
		field.JSON("pii_summary", map[string]interface{}{}).Optional(),
		field.Time("created_at").Default(time.Now),
	}
}

// Edges of the Document
func (Document) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tenant", Tenant.Type).
			Ref("documents").
			Field("tenant_id").
			Required().
			Unique(),
		edge.To("chunks", Chunk.Type),
	}
}

// Indexes of the Document
func (Document) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "source_url"),
		index.Fields("tenant_id", "normalized_hash"),
		index.Fields("tenant_id", "title"),
	}
}
