package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	pb "github.com/qdrant/go-client/qdrant"
	"github.com/rs/xid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// QdrantClient implements vector store operations with Qdrant
type QdrantClient struct {
	url                  string
	apiKey               string
	collection           string
	embeddingDim         int
	destructiveMigration bool
	docCollection        string
	logger               *zap.Logger
	conn                 *grpc.ClientConn
	points               pb.PointsClient
	collections          pb.CollectionsClient
}

// NewQdrantClient creates a new Qdrant client
func NewQdrantClient(rawURL, apiKey, collection string, embeddingDim int, destructiveMigration bool, logger *zap.Logger) (*QdrantClient, error) {
	grpcAddr, useTLS := parseGRPCAddr(rawURL)

	// Common pitfall: hitting the REST port (6333) with gRPC causes "frame too large" errors.
	if strings.HasSuffix(grpcAddr, ":6333") {
		logger.Warn("Qdrant gRPC expects port 6334; switching from HTTP port 6333 to 6334 to avoid preface errors", zap.String("addr", grpcAddr))
		grpcAddr = strings.TrimSuffix(grpcAddr, ":6333") + ":6334"
	}

	// Build interceptors to inject API key when provided
	var dialOpts []grpc.DialOption
	dialOpts = append(dialOpts, grpc.WithUnaryInterceptor(apiKeyUnaryInterceptor(apiKey)))
	dialOpts = append(dialOpts, grpc.WithStreamInterceptor(apiKeyStreamInterceptor(apiKey)))

	if useTLS {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, "")))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.Dial(grpcAddr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Qdrant: %w", err)
	}

	client := &QdrantClient{
		url:                  rawURL,
		apiKey:               apiKey,
		collection:           collection,
		embeddingDim:         embeddingDim,
		destructiveMigration: destructiveMigration,
		docCollection:        collection + "_docs",
		logger:               logger.Named("qdrant"),
		conn:                 conn,
		points:               pb.NewPointsClient(conn),
		collections:          pb.NewCollectionsClient(conn),
	}

	// Ensure collection exists
	if err := client.ensureCollection(context.Background()); err != nil {
		logger.Warn("failed to ensure collection exists", zap.Error(err))
	}
	if err := client.ensureDocCollection(context.Background()); err != nil {
		logger.Warn("failed to ensure doc collection exists", zap.Error(err))
	}

	return client, nil
}

// ensureCollection creates the collection if it doesn't exist and validates vector size.
func (q *QdrantClient) ensureCollection(ctx context.Context) error {
	return q.ensureNamedCollection(ctx, q.collection)
}

// ensureDocCollection creates the doc-level embedding collection if it doesn't exist and validates vector size.
func (q *QdrantClient) ensureDocCollection(ctx context.Context) error {
	return q.ensureNamedCollection(ctx, q.docCollection)
}

func (q *QdrantClient) ensureNamedCollection(ctx context.Context, name string) error {
	info, err := q.collections.Get(ctx, &pb.GetCollectionInfoRequest{
		CollectionName: name,
	})
	if err == nil {
		if size, ok := extractVectorSize(info); ok {
			expected := uint64(q.embeddingDim)
			if size != expected {
				if q.destructiveMigration {
					q.logger.Warn(
						"collection vector size mismatch; recreating",
						zap.String("collection", name),
						zap.Uint64("existing_dim", size),
						zap.Uint64("expected_dim", expected),
					)
					if err := q.deleteCollection(ctx, name); err != nil {
						return fmt.Errorf("failed to delete collection %s: %w", name, err)
					}
					return q.createCollection(ctx, name)
				}
				return fmt.Errorf(
					"collection %s vector size %d does not match expected %d (set VECTORSTORE_DESTRUCTIVE_MIGRATION=true to recreate)",
					name,
					size,
					expected,
				)
			}
		} else {
			q.logger.Warn("collection vector size unavailable; skipping dimension check", zap.String("collection", name))
		}
		q.logger.Debug("collection already exists", zap.String("collection", name))
		return nil
	}

	return q.createCollection(ctx, name)
}

func (q *QdrantClient) createCollection(ctx context.Context, name string) error {
	q.logger.Info("creating collection", zap.String("collection", name))
	_, err := q.collections.Create(ctx, &pb.CreateCollection{
		CollectionName: name,
		VectorsConfig: &pb.VectorsConfig{
			Config: &pb.VectorsConfig_Params{
				Params: &pb.VectorParams{
					Size:     uint64(q.embeddingDim),
					Distance: pb.Distance_Cosine,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create collection %s: %w", name, err)
	}
	q.logger.Info("collection created", zap.String("collection", name))
	return nil
}

func (q *QdrantClient) deleteCollection(ctx context.Context, name string) error {
	q.logger.Warn("deleting collection", zap.String("collection", name))
	_, err := q.collections.Delete(ctx, &pb.DeleteCollection{
		CollectionName: name,
	})
	if err != nil {
		return err
	}
	q.logger.Info("collection deleted", zap.String("collection", name))
	return nil
}

func extractVectorSize(info *pb.GetCollectionInfoResponse) (uint64, bool) {
	if info == nil || info.GetResult() == nil || info.GetResult().GetConfig() == nil || info.GetResult().GetConfig().GetParams() == nil {
		return 0, false
	}

	vectorsConfig := info.GetResult().GetConfig().GetParams().GetVectorsConfig()
	if vectorsConfig == nil {
		return 0, false
	}

	if params := vectorsConfig.GetParams(); params != nil {
		return params.GetSize(), true
	}

	if paramsMap := vectorsConfig.GetParamsMap(); paramsMap != nil {
		if params, ok := paramsMap.GetMap()["default"]; ok && params != nil {
			return params.GetSize(), true
		}
		for _, params := range paramsMap.GetMap() {
			if params != nil {
				return params.GetSize(), true
			}
		}
	}

	return 0, false
}

// Upsert inserts or updates chunks in the vector store
func (q *QdrantClient) Upsert(ctx context.Context, chunks []shared.Chunk) error {
	q.logger.Info("upserting chunks", zap.Int("count", len(chunks)))

	if len(chunks) == 0 {
		return nil
	}

	points := make([]*pb.PointStruct, len(chunks))
	for i, chunk := range chunks {
		// Convert float64 embedding to float32
		embedding := make([]float32, len(chunk.Embedding))
		for j, v := range chunk.Embedding {
			embedding[j] = float32(v)
		}

		// Create payload
		payload := map[string]*pb.Value{
			"text":        {Kind: &pb.Value_StringValue{StringValue: chunk.Text}},
			"document_id": {Kind: &pb.Value_StringValue{StringValue: chunk.DocumentID.String()}},
			"chunk_id":    {Kind: &pb.Value_StringValue{StringValue: chunk.ID}},
			"tenant_id":   {Kind: &pb.Value_StringValue{StringValue: chunk.TenantID.String()}},
		}

		// Store metadata as JSON string + promote filterable keys as top-level payload fields
		if chunk.Metadata != nil && len(chunk.Metadata) > 0 {
			metaBytes, err := json.Marshal(chunk.Metadata)
			if err == nil {
				payload["metadata"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: string(metaBytes)}}
			}
			// Promote well-known metadata keys as top-level Qdrant payload fields for native filtering.
			// After re-ingestion, these fields become filterable without post-processing.
			filterableKeys := []string{"offre_id", "offreId", "consultation_id", "consultationId", "trigramme", "doc_type", "file_name"}
			for _, key := range filterableKeys {
				if val, ok := chunk.Metadata[key]; ok {
					if strVal := metadataValueToString(val); strVal != "" {
						payload["meta_"+key] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: strVal}}
					}
				}
			}
		}

		// Convert chunk ID to uint64 using hash
		pointID := hashStringToUint64(chunk.ID)

		points[i] = &pb.PointStruct{
			Id: &pb.PointId{
				PointIdOptions: &pb.PointId_Num{Num: pointID},
			},
			Vectors: &pb.Vectors{
				VectorsOptions: &pb.Vectors_Vector{
					Vector: &pb.Vector{Data: embedding},
				},
			},
			Payload: payload,
		}
	}

	// Upsert points
	waitUpsert := true
	_, err := q.points.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: q.collection,
		Wait:           &waitUpsert,
		Points:         points,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert points: %w", err)
	}

	q.logger.Info("chunks upserted successfully", zap.Int("count", len(chunks)))
	return nil
}

// UpsertDocEmbedding inserts or updates a doc-level embedding in the doc collection
func (q *QdrantClient) UpsertDocEmbedding(ctx context.Context, doc shared.Chunk) error {
	embedding := make([]float32, len(doc.Embedding))
	for j, v := range doc.Embedding {
		embedding[j] = float32(v)
	}

	payload := map[string]*pb.Value{
		"document_id": {Kind: &pb.Value_StringValue{StringValue: doc.DocumentID.String()}},
		"tenant_id":   {Kind: &pb.Value_StringValue{StringValue: doc.TenantID.String()}},
	}
	if doc.Metadata != nil {
		if dt, ok := doc.Metadata["doc_type"].(string); ok {
			payload["doc_type"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: dt}}
		}
	}

	pointID := hashStringToUint64(doc.DocumentID.String())

	waitUpsert := true
	_, err := q.points.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: q.docCollection,
		Wait:           &waitUpsert,
		Points: []*pb.PointStruct{
			{
				Id: &pb.PointId{PointIdOptions: &pb.PointId_Num{Num: pointID}},
				Vectors: &pb.Vectors{
					VectorsOptions: &pb.Vectors_Vector{
						Vector: &pb.Vector{Data: embedding},
					},
				},
				Payload: payload,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to upsert doc embedding: %w", err)
	}
	return nil
}

// Search performs vector search
func (q *QdrantClient) Search(ctx context.Context, query string, topK int, tenantID xid.ID) ([]shared.Chunk, error) {
	q.logger.Debug("searching vectors", zap.String("query", query), zap.Int("topK", topK), zap.String("tenantID", tenantID.String()))
	// This method receives a text query but needs an embedding
	// The retriever should generate the embedding and call SearchByVector
	return []shared.Chunk{}, nil
}

// SearchByVector performs vector search using an embedding
func (q *QdrantClient) SearchByVector(ctx context.Context, embedding []float64, topK int, tenantID xid.ID, metadataFilters map[string]string) ([]shared.Chunk, error) {
	q.logger.Debug("searching by vector", zap.Int("topK", topK), zap.Any("metadataFilters", metadataFilters))

	// Convert float64 to float32
	queryVector := make([]float32, len(embedding))
	for i, v := range embedding {
		queryVector[i] = float32(v)
	}

	// Build filter conditions — always filter by tenant_id
	mustConditions := []*pb.Condition{
		{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "tenant_id",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: tenantID.String()},
					},
				},
			},
		},
	}

	// Add metadata filter conditions (native Qdrant filtering on promoted fields).
	// Keys are tried with "meta_" prefix first (promoted fields from re-ingestion).
	// Dual-key support: e.g. "offre_id" maps to both "meta_offre_id" and "meta_offreId".
	keyAliases := map[string][]string{
		"offre_id":        {"meta_offre_id", "meta_offreId"},
		"offreId":         {"meta_offre_id", "meta_offreId"},
		"consultation_id": {"meta_consultation_id", "meta_consultationId"},
		"consultationId":  {"meta_consultation_id", "meta_consultationId"},
		"trigramme":       {"meta_trigramme"},
		"doc_type":        {"meta_doc_type"},
		"file_name":       {"meta_file_name"},
	}

	for filterKey, filterVal := range metadataFilters {
		aliases, ok := keyAliases[filterKey]
		if !ok {
			// Unknown key: try with meta_ prefix
			aliases = []string{"meta_" + filterKey}
		}
		// Use Should (OR) across aliases so either naming convention matches
		if len(aliases) == 1 {
			mustConditions = append(mustConditions, &pb.Condition{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: aliases[0],
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{Keyword: filterVal},
						},
					},
				},
			})
		} else {
			// OR across aliases
			shouldConds := make([]*pb.Condition, 0, len(aliases))
			for _, alias := range aliases {
				shouldConds = append(shouldConds, &pb.Condition{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: alias,
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{Keyword: filterVal},
							},
						},
					},
				})
			}
			mustConditions = append(mustConditions, &pb.Condition{
				ConditionOneOf: &pb.Condition_Filter{
					Filter: &pb.Filter{
						Should: shouldConds,
					},
				},
			})
		}
	}

	searchPoints := func(limit int, mustConditions []*pb.Condition) (*pb.SearchResponse, error) {
		return q.points.Search(ctx, &pb.SearchPoints{
			CollectionName: q.collection,
			Vector:         queryVector,
			Limit:          uint64(limit),
			Filter: &pb.Filter{
				Must: mustConditions,
			},
			WithPayload: &pb.WithPayloadSelector{
				SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true},
			},
		})
	}

	// Search with native metadata filters first.
	result, err := searchPoints(topK, mustConditions)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	// Fallback for legacy chunks that have metadata JSON but no promoted meta_* fields yet.
	// Retrieve a larger tenant-only candidate set; pipeline post-filtering will narrow it down.
	if len(result.Result) == 0 && len(metadataFilters) > 0 {
		fallbackLimit := topK * 50
		if fallbackLimit < 200 {
			fallbackLimit = 200
		}
		if fallbackLimit > 1000 {
			fallbackLimit = 1000
		}
		q.logger.Debug("retrying vector search without native metadata filters",
			zap.Int("topK", topK),
			zap.Int("fallback_limit", fallbackLimit),
			zap.Any("metadataFilters", metadataFilters))
		result, err = searchPoints(fallbackLimit, mustConditions[:1])
	}
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	// Convert results to chunks
	chunks := make([]shared.Chunk, len(result.Result))
	for i, point := range result.Result {
		docID, _ := xid.FromString(getStringPayload(point.Payload, "document_id"))
		chunk := shared.Chunk{
			ID:         getStringPayload(point.Payload, "chunk_id"),
			Text:       getStringPayload(point.Payload, "text"),
			DocumentID: docID,
			TenantID:   tenantID,
			Score:      float64(point.Score),
		}

		// Parse metadata from JSON string
		if metaStr := getStringPayload(point.Payload, "metadata"); metaStr != "" {
			var metadata map[string]interface{}
			if err := json.Unmarshal([]byte(metaStr), &metadata); err == nil {
				chunk.Metadata = metadata
			}
		}

		chunks[i] = chunk
	}

	q.logger.Debug("search completed", zap.Int("results", len(chunks)))
	return chunks, nil
}

// SearchDocEmbedding performs doc-level vector search
func (q *QdrantClient) SearchDocEmbedding(ctx context.Context, embedding []float64, topK int, tenantID xid.ID) ([]shared.Chunk, error) {
	q.logger.Debug("searching doc embeddings", zap.Int("topK", topK))

	queryVector := make([]float32, len(embedding))
	for i, v := range embedding {
		queryVector[i] = float32(v)
	}

	result, err := q.points.Search(ctx, &pb.SearchPoints{
		CollectionName: q.docCollection,
		Vector:         queryVector,
		Limit:          uint64(topK),
		Filter: &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "tenant_id",
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{Keyword: tenantID.String()},
							},
						},
					},
				},
			},
		},
		WithPayload: &pb.WithPayloadSelector{
			SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search doc embeddings: %w", err)
	}

	chunks := make([]shared.Chunk, len(result.Result))
	for i, point := range result.Result {
		docID := mustXID(getStringPayload(point.Payload, "document_id"))
		chunks[i] = shared.Chunk{
			DocumentID: docID,
			TenantID:   tenantID,
			Score:      float64(point.Score),
			Metadata: map[string]interface{}{
				"doc_type": getStringPayload(point.Payload, "doc_type"),
			},
		}
	}
	return chunks, nil
}

// getStringPayload extracts a string value from payload
func getStringPayload(payload map[string]*pb.Value, key string) string {
	if v, ok := payload[key]; ok {
		if sv, ok := v.Kind.(*pb.Value_StringValue); ok {
			return sv.StringValue
		}
	}
	return ""
}

func metadataValueToString(val interface{}) string {
	switch v := val.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case float32:
		if v == float32(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case int:
		return fmt.Sprintf("%d", v)
	case int8:
		return fmt.Sprintf("%d", v)
	case int16:
		return fmt.Sprintf("%d", v)
	case int32:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case uint:
		return fmt.Sprintf("%d", v)
	case uint8:
		return fmt.Sprintf("%d", v)
	case uint16:
		return fmt.Sprintf("%d", v)
	case uint32:
		return fmt.Sprintf("%d", v)
	case uint64:
		return fmt.Sprintf("%d", v)
	case json.Number:
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

// hashStringToUint64 converts a string to uint64 using FNV-1a hash
func hashStringToUint64(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func mustXID(s string) xid.ID {
	id, err := xid.FromString(s)
	if err != nil {
		return xid.NilID()
	}
	return id
}

// DeleteByTenant deletes all vectors for a tenant from chunk and doc collections.
func (q *QdrantClient) DeleteByTenant(ctx context.Context, tenantID xid.ID) error {
	if tenantID == xid.NilID() {
		return fmt.Errorf("tenant_id is required for vector deletion")
	}
	if err := q.deleteByTenant(ctx, q.collection, tenantID); err != nil {
		return err
	}
	if err := q.deleteByTenant(ctx, q.docCollection, tenantID); err != nil {
		return err
	}
	return nil
}

func (q *QdrantClient) deleteByTenant(ctx context.Context, collection string, tenantID xid.ID) error {
	q.logger.Info("deleting tenant vectors",
		zap.String("collection", collection),
		zap.String("tenantID", tenantID.String()))

	waitDelete := true
	_, err := q.points.Delete(ctx, &pb.DeletePoints{
		CollectionName: collection,
		Wait:           &waitDelete,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{
				Filter: &pb.Filter{
					Must: []*pb.Condition{
						{
							ConditionOneOf: &pb.Condition_Field{
								Field: &pb.FieldCondition{
									Key: "tenant_id",
									Match: &pb.Match{
										MatchValue: &pb.Match_Keyword{Keyword: tenantID.String()},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete tenant vectors from %s: %w", collection, err)
	}

	return nil
}

// Delete deletes chunks from the vector store
func (q *QdrantClient) Delete(ctx context.Context, chunkIDs []string) error {
	q.logger.Info("deleting chunks", zap.Int("count", len(chunkIDs)))

	if len(chunkIDs) == 0 {
		return nil
	}

	pointIDs := make([]*pb.PointId, len(chunkIDs))
	for i, id := range chunkIDs {
		pointIDs[i] = &pb.PointId{
			PointIdOptions: &pb.PointId_Num{Num: hashStringToUint64(id)},
		}
	}

	waitDelete := true
	_, err := q.points.Delete(ctx, &pb.DeletePoints{
		CollectionName: q.collection,
		Wait:           &waitDelete,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Points{
				Points: &pb.PointsIdsList{Ids: pointIDs},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete points: %w", err)
	}

	q.logger.Info("chunks deleted successfully", zap.Int("count", len(chunkIDs)))
	return nil
}

// Close closes the gRPC connection
func (q *QdrantClient) Close() error {
	if q.conn != nil {
		return q.conn.Close()
	}
	return nil
}

// parseGRPCAddr extracts host:port and whether to use TLS from a URL-style string.
func parseGRPCAddr(raw string) (addr string, useTLS bool) {
	if raw == "" {
		return "localhost:6334", false
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// Assume raw is already host:port
		addr = raw
	} else {
		addr = u.Host
		if !strings.Contains(addr, ":") {
			addr = addr + ":6334"
		}
		useTLS = u.Scheme == "https" || u.Scheme == "grpcs" || u.Scheme == "tls"
	}

	if addr == "" {
		addr = "localhost:6334"
	}

	return addr, useTLS
}

func apiKeyUnaryInterceptor(apiKey string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if apiKey != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "api-key", apiKey)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func apiKeyStreamInterceptor(apiKey string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if apiKey != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "api-key", apiKey)
		}
		return streamer(ctx, desc, cc, method, opts...)
	}
}
