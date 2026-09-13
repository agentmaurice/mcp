package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/rs/xid"
)

// Deployment holds the schema definition for the Deployment entity
type Deployment struct {
	ent.Schema
}

// Fields of the Deployment
func (Deployment) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			GoType(xid.ID{}).
			DefaultFunc(xid.New),
		field.String("name").Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Deployment
func (Deployment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("tenants", Tenant.Type),
	}
}
