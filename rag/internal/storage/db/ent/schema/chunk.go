package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/rs/xid"
)

// Chunk holds the schema definition for the Chunk entity
type Chunk struct {
	ent.Schema
}

// Fields of the Chunk
func (Chunk) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			GoType(xid.ID{}).
			DefaultFunc(xid.New),
		field.String("document_id").GoType(xid.ID{}),
		field.Text("text").NotEmpty(),
		field.JSON("metadata", map[string]interface{}{}).Optional(),
		field.String("embedding_model").Optional(),
		field.String("vector_id").Optional(), // ID in vector store
		field.Bytes("embedding_int8").Optional(),
		field.Bytes("embedding_binary").Optional(),
	}
}

// Edges of the Chunk
func (Chunk) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("document", Document.Type).
			Ref("chunks").
			Field("document_id").
			Required().
			Unique(),
	}
}
