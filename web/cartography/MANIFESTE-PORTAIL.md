# Manifeste des requêtes persistées — brique Cartography du portail

Pour le relais `sh-onboard` (session `sop`). Le navigateur n'envoie **jamais** de texte
GraphQL : il poste `{"id":"<nom>","variables":{…}}` sur `/cartography/graphql`, et le
relais détient les textes ci-dessous.

## Les quatre documents

### `entities`

```graphql
query Entities($first: Int, $after: String) {
  entities(first: $first, after: $after) {
    edges { node { id identityFingerprint type deleted displayName identity { key value } attributes { key value } } }
    pageInfo { hasNextPage endCursor }
  }
}
```

**`identityFingerprint` est le champ de citation**, et c'est lui qu'un agent doit renvoyer,
jamais `id`. L'`id` est **local au réplica qui répond** : il diffère entre réplicas et il est
reforgé quand une entité revient après un long silence (ADR 0035). La production tourne sur
**deux réplicas derrière un tour de rôle DNS**, donc des citations par `id` auraient désigné
des entités introuvables **par intermittence** — le pire mode de panne, qui marche à la
répétition et rate en séance. L'empreinte, elle, est calculée identiquement par tous les
réplicas.

**Format, mesuré sur 21 entités de 7 types** : `<type>:<32 hexadécimaux minuscules>`, par
exemple `host:f31b9158b93d2b8c350906e6c471e7ac`. **Longueurs de 35 à 50 caractères**, le
maximum théorique étant 50 (`network.interface`, le plus long nom de type du registre, plus
`:` plus 32). Le filtre `[A-Za-z0-9:._-]{1,128}` du portail les accepte toutes.

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

### `graphScope`

```graphql
query GraphScope {
  graphScope { tenant tenantName entities relations newestEvent }
}
```

Ce document est lu **en premier**, avant les données, et son périmètre est
affiché en permanence. Un parc vide s'affiche « 0 entities, no events yet »
au lieu d'une toile blanche. La page compare ensuite ce qu'elle a chargé à
ces compteurs : en dessous, elle dessine quand même et affiche
« Incomplete: entities X of Y ». C'est cette comparaison qui permet de
détecter **toute** lecture tronquée — une page en échec, une limite
atteinte, un curseur périmé, ou une page vide se déclarant la dernière —
et pas seulement les cas qu'on a su prévoir.

#### Les compteurs doivent être NETS de ce que le relais retire

Le relais retire des entités des réponses `entities` — aujourd'hui les
`host.service`, pour les deux raisons déjà écrites plus haut dans ce
manifeste. Or `graphScope` est servi par le serveur, qui ne sait rien de ce
filtrage : ses compteurs portent sur **tout** le parc.

Mesuré en production sur un tenant réel : 2 328 entités annoncées par
`graphScope`, dont 1 244 `host.service`, et 1 084 reçues par l'énumération.
L'écart vaut exactement les entités retirées. **Une comparaison naïve
afficherait donc un « Incomplete » permanent et faux**, sur un parc
entièrement chargé.

Le relais doit donc rendre des compteurs **nets** : il soustrait, pour
chaque type qu'il retire, le `totalCount` d'une requête comptée en interne
et jamais exposée au navigateur :

```graphql
query CountOfType($type: String!) {
  entities(filter: {type: $type}, first: 0) { totalCount }
}
```

`first: 0` est délibéré : `totalCount` est le compte complet du filtre,
indépendant de la page, donc demander zéro arête évite de transférer
cinquante nœuds inutiles à chaque rafraîchissement de périmètre. La valeur
est la même avant et après la 0.24.0, bien qu'elle soit calculée par deux
chemins différents — vérifié en recette, la somme des `totalCount` par
type égale les compteurs de `graphScope` à l'unité sur une connexion
directe.

#### Le filtrage doit être déclaré en clair

Un compteur net rend la détection honnête, mais il rend le filtrage
invisible : rien ne dit quels types ont été écartés. La phrase qui les
nomme rend le filtrage visible, mais sans le compteur net elle donne un
faux écart. Les deux sont donc nécessaires : le compteur net sans la
phrase donne un périmètre muet, la phrase sans le compteur net donne un
faux écart.

C'est le relais qui porte cette obligation, car il est le seul à savoir
ce qu'il retire : le filtrage est appliqué par lui, après la réponse du
serveur, et rien dans ce que la page reçoit ne lui indique quels types
ont été écartés. Le relais rend donc la phrase avec la page qu'il sert,
par exemple « System units are left out ». La page, elle, se contente de
l'afficher là où le relais la place.

La règle générale derrière ce cas est simple : celui qui filtre est
celui qui déclare. Un acteur qui retire quelque chose d'une réponse est
le seul à pouvoir nommer ce qu'il a retiré, donc l'obligation suit le
filtrage et ne se délègue pas en aval.

#### La variable `asOf`

| variable | type | validation attendue |
|----------|------|---------------------|
| `asOf` | `String`, RFC 3339 à la seconde, **optionnel** | dans la fenêtre annoncée par `graphScope` (`oldestAnswerable` à maintenant), et issue d'une liste fermée ou d'une grille |
| `from` | `String`, instant RFC 3339 | uniquement sur `recentChanges` ; doit être ≤ `to` |
| `to` | `String`, instant RFC 3339 | uniquement sur `recentChanges` ; doit être ≥ `from` |

`asOf` est accepté sur `entities`, `relations` et `graphScope`. Il n'est
**envoyé que s'il est non nul** : un `asOf: null` explicite n'est pas
équivalent à une absence pour un validateur strict.

Pourquoi ces bornes : sans elles, un glisser de curseur fabriquerait des
centaines d'instants distincts et autant de rejouements. Et pour
`recentChanges`, `from` et `to` sont des instants absolus — lors d'une
relecture du passé, une fenêtre **relative** compterait depuis maintenant et
afficherait la mauvaise période.

Deux pièges de filtrage à retenir : le relais doit écarter les mêmes types
**dans le passé**, et calculer le compte net **au même `asOf`** — sinon
l'avertissement d'incomplétude retentirait en permanence dès qu'un lecteur
remonte dans le temps.

## Ce que le relais doit faire en plus

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
