# Technologies utilisées

## Base de données

### PostgreSQL
Base de données relationnelle pour stocker :
- Les métadonnées des documents
- Les jobs d'ingestion
- Les informations des tenants

### Qdrant
Base de données vectorielle pour :
- Stockage des embeddings
- Recherche par similarité vectorielle
- Gestion de collections multi-tenants

## Modèles de langage

### Embeddings
Utilisation de `text-embedding-ada-002` pour générer les embeddings vectoriels des chunks de documents.

### Génération
Support de modèles OpenAI-compatibles :
- GPT-4 pour les réponses de haute qualité
- GPT-3.5-turbo pour des réponses plus rapides
- Possibilité d'utiliser des modèles locaux via API compatible

## Framework et outils

### Go
Langage principal avec :
- Ent ORM pour la gestion de la base de données
- Fiber pour le serveur HTTP
- Viper pour la configuration
- Zap pour le logging structuré

### MCP (Model Context Protocol)
Protocole standardisé pour l'interaction avec les systèmes d'IA.
