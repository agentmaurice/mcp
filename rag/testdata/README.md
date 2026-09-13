# Données de test pour le système RAG

Ce répertoire contient des documents de test pour valider le système RAG.

## Structure

```
testdata/
├── documents/          # Documents à ingérer
│   ├── sample1.md     # Introduction au RAG
│   ├── sample2.md     # Architecture du système
│   └── sample3.md     # Technologies utilisées
└── README.md          # Ce fichier
```

## Ajouter des documents de test

Pour ajouter vos propres documents de test :

1. Créez un fichier `.md`, `.txt` ou autre dans le répertoire `documents/`
2. Le nom du fichier (sans extension) sera utilisé comme titre
3. Le contenu sera automatiquement découpé en chunks par le système

### Exemple

```markdown
# Mon document de test

Ceci est le contenu de mon document qui sera ingéré dans le système RAG.

## Section 1
Contenu de la première section...

## Section 2
Contenu de la deuxième section...
```

## Utilisation

### 1. Ingérer les données de test

```bash
# Ingérer tous les documents avec un tenant par défaut
task test-ingest

# Ingérer avec un tenant spécifique
task test-ingest -- my-tenant-id
```

Ou directement avec le script :

```bash
./scripts/ingest_test_data.sh [tenant_id]
```

### 2. Tester une requête

```bash
# Tester une requête simple
task test-query -- "Qu'est-ce que le RAG?"

# Tester avec un tenant spécifique
task test-query -- "Qu'est-ce que le RAG?" my-tenant-id
```

Ou directement avec le script :

```bash
./scripts/test_query.sh "Ma question" [tenant_id]
```

### 3. Test end-to-end

Pour tester l'ensemble du processus (ingestion + requête) :

```bash
task test-e2e
```

Cette commande va :
1. Ingérer tous les documents de test
2. Exécuter une requête de test
3. Afficher les résultats avec citations

## Scripts disponibles

### `scripts/ingest_test_data.sh`

Ingère tous les fichiers `.md` du répertoire `testdata/documents/`.

**Options :**
- `tenant_id` (optionnel) : ID du tenant (défaut: `test-tenant-001`)

**Variables d'environnement :**
- `RAG_API_URL` : URL de l'API RAG (défaut: `http://localhost:8080`)

### `scripts/test_query.sh`

Exécute une requête RAG et affiche les résultats.

**Arguments :**
- `query` : Question à poser
- `tenant_id` (optionnel) : ID du tenant (défaut: `test-tenant-001`)

**Variables d'environnement :**
- `RAG_API_URL` : URL de l'API RAG (défaut: `http://localhost:8080`)

## Exemples de requêtes

Une fois les données ingérées, vous pouvez tester ces requêtes :

```bash
# Questions sur le RAG
task test-query -- "Qu'est-ce que le RAG?"
task test-query -- "Quels sont les avantages du RAG?"

# Questions sur l'architecture
task test-query -- "Décris les 5 couches du pipeline RAG"
task test-query -- "Qu'est-ce que la Layer 2?"

# Questions sur les technologies
task test-query -- "Quelles technologies sont utilisées?"
task test-query -- "Pourquoi utiliser Qdrant?"
```

## Format des documents

Les documents peuvent être au format :
- Markdown (`.md`) - recommandé
- Texte brut (`.txt`)
- JSON (pour des données structurées)

Le système détecte automatiquement la structure et découpe intelligemment le contenu en chunks optimaux.
