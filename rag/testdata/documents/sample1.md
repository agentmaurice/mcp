# Introduction au RAG

Le RAG (Retrieval-Augmented Generation) est une technique qui combine la recherche d'informations avec la génération de texte par des modèles de langage.

## Composants principaux

### Retrieval (Recherche)
La phase de recherche utilise des embeddings vectoriels pour trouver les documents les plus pertinents par rapport à une requête utilisateur.

### Augmentation
Les documents récupérés sont utilisés pour augmenter le contexte fourni au modèle de langage.

### Génération
Le modèle de langage génère une réponse en se basant sur les informations récupérées.

## Avantages

- Réponses basées sur des faits vérifiables
- Réduction des hallucinations
- Possibilité de mettre à jour la base de connaissances sans réentraîner le modèle
