# Manifeste des requêtes persistées — brique Cartography du portail

Pour le relais `sh-onboard` (session `sop`). Le navigateur n'envoie **jamais** de texte
GraphQL : il poste `{"id":"<nom>","variables":{…}}` sur `/cartography/graphql`, et le
relais détient les textes ci-dessous.

## Les trois documents

### `entities`

```graphql
query Entities($first: Int, $after: String) {
  entities(first: $first, after: $after) {
    edges { node { id type deleted displayName identity { key value } attributes { key value } } }
    pageInfo { hasNextPage endCursor }
  }
}
```

| variable | type | validation attendue |
|---|---|---|
| `first` | `Int` | **200 au plus** — le serveur plafonne de toute façon chaque page à 200 |
| `after` | `String` | curseur opaque, facultatif |

### `relations`

```graphql
query Relations($first: Int, $after: String) {
  relations(first: $first, after: $after) {
    edges { node { id type fromId toId } }
    pageInfo { hasNextPage endCursor }
  }
}
```

Mêmes variables et mêmes bornes que `entities`.

### `recentChanges`

```graphql
query RecentChanges($window: String, $first: Int, $after: String) {
  recentChanges(window: $window, first: $first, after: $after, includeHeartbeats: false) {
    edges { node { eventTime changeType entity { type } } }
    pageInfo { hasNextPage endCursor }
  }
}
```

| variable | type | validation attendue |
|---|---|---|
| `window` | `String` | durée en secondes suffixée `s`, par exemple `86400s`. **Liste fermée conseillée** : `3600s`, `21600s`, `86400s`, `604800s` |
| `first` | `Int` | 200 au plus |
| `after` | `String` | curseur opaque, facultatif |

`includeHeartbeats` est **écrit en dur à `false`** dans le document : un battement n'est pas
un changement et il noierait le signal.

## Ce que le relais doit faire en plus

1. **imposer le tenant** depuis la session du portail, et **retirer** `?tenant=` et
   `X-Scope-OrgID`. La page ne les envoie plus, c'est une défense en profondeur ;
2. **retirer `entity.owner.contact`** et tout attribut de contact des réponses. Mesuré :
   deux adresses personnelles réelles sur 10 des 20 hôtes du tenant 1. Un masquage côté
   page serait sans effet, la donnée arrivant dans le navigateur ;
3. **retirer les entités de type `host.service`** des réponses `entities`. Deux raisons :
   elles nomment ce que nous surveillons pour des tiers (`senhub-sensor-pegelonline`,
   `-dynamics365`, `-o365`, `-digitalocean`), et elles sont 1241 sur ~1700, donc illisibles.
   **Les relations qui les visent peuvent rester** : la page ne dessine une arête que si
   ses deux extrémités sont présentes ;
4. **refuser toute variable inconnue**, et toute requête hors de ces trois identifiants.

## Pagination

`entities(first:N)` plafonne à **200** côté serveur, et la page suit le curseur exprès :
au-delà, le graphe serait tronqué et incohérent, avec des nœuds orphelins dont l'hôte est
sur une page suivante. Le **même identifiant est donc appelé plusieurs fois** avec un
`after` différent. Sur le tenant 1, environ **trois appels `entities`** après le retrait des
`host.service`, et un à deux `relations`.

## Ce que la page n'envoie plus

- **aucune mutation** : Toise n'en a qu'une, `annotateEntity`, absente de ce manifeste ;
- **aucun abonnement** : `entityChanged` et `relationChanged` sont absents ; la page
  rafraîchit par scrutation toutes les 6 secondes ;
- **aucun `asOf`** : la frise temporelle est masquée dans cette version. Chaque glissement
  déclenchait une lecture à un instant passé, dont le coût suit la taille du journal et qui
  rend 503 sous charge ;
- **aucun tenant** : plus de `?tenant=`, plus d'en-tête, et le champ de saisie du tenant a
  été retiré de l'interface — dans un portail cloisonné, c'était une invitation à essayer ;
- **aucun point d'accès variable** : `/cartography/graphql` est figé dans la page, la
  surcharge par `?api=` est supprimée.

## La bibliothèque de rendu

`vendor/vis-network.min.js`, à placer par le déploiement, à côté de la page.
Version **vis-network 9.1.9**, intégrité publiée du paquet
`sha384-yxKDWWf0wwdUj/gPeuL11czrnKFQROnLgY8ll7En9NYoXibgg3C6NK/UDHNtUgWJ`.
Servie en local et non depuis un CDN : sans sortie internet, l'écran est blanc sans erreur.
