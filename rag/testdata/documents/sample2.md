# Architecture du système RAG

## Pipeline en 5 couches

### Layer 0: Document Intelligence
Analyse intelligente des documents et découpage en chunks optimaux. Cette couche gère :
- La détection de la structure du document
- Le chunking sémantique
- L'extraction de métadonnées

### Layer 1: Query Intelligence
Analyse de l'intention de l'utilisateur et extraction de mots-clés. Comprend :
- Détection de l'intent (question, recherche, exploration)
- Extraction de mots-clés pertinents
- Détection de la langue

### Layer 2: Retrieval
Recherche hybride combinant :
- Recherche vectorielle (embeddings)
- Recherche en texte intégral
- Reranking des résultats

### Layer 3: Reasoning
Génération de réponses par LLM avec :
- Construction du prompt avec contexte
- Génération de la réponse
- Création des citations

### Layer 4: Experience
Logging des interactions pour l'amélioration continue :
- Stockage des requêtes et réponses
- Collecte de feedback utilisateur
- Analyse pour optimisation
