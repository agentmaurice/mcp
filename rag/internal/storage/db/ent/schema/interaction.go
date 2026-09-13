package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/rs/xid"
)

// Interaction holds the schema definition for logging interactions and feedback.
type Interaction struct {
	ent.Schema
}

// Fields of the Interaction.
func (Interaction) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			GoType(xid.ID{}).
			DefaultFunc(xid.New),
		field.String("deployment_id").GoType(xid.ID{}).Optional().Nillable(),
		field.String("tenant_id").GoType(xid.ID{}),
		field.String("event_type"), // interaction | feedback
		field.Text("query"),
		field.Text("answer").Optional(),
		field.Bool("helpful").Optional(),
		field.Int("duration_ms").Optional(),
		field.Time("created_at").Default(time.Now),
	}
}

// Edges of the Interaction.
func (Interaction) Edges() []ent.Edge {
	return nil
}
