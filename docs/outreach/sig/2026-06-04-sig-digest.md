# SIG digest — Resources & Entities — 2026-06-04

Premier digest de la liaison SIG. État du paysage spec réactualisé, logistique
du SIG, et recommandation d'angle d'intervention pour Toise.

Sources principales : OTEP 0256, spec PR #4836, #5067, #5057, #4956, repo
`open-telemetry/community`, OTEP README. Liens en bas.

> **MISE À JOUR fin de journée 2026-06-04.** Re-vérification : **#4836 A MERGÉ
> aujourd'hui** (merge queue, commit `d42d1af`) — il n'est plus « approuvé non
> mergé ». La section 1 ci-dessous a été corrigée en conséquence ; les passages
> antérieurs sont datés. Faits nouveaux intégrés : le « patch event » est
> désormais **nommé dans le texte mergé**, le fichier de spec est
> `specification/entities/entity-events.md`, `entity.id` resserré à
> `map<string,string>`, `entity.report.interval` en **secondes**. Voir l'addendum
> en fin de section 1.

---

## 1. Quoi de neuf (delta depuis l'état décrit dans les instructions)

### Cadre général inchangé, mais le travail s'est déplacé vers l'**identité** et le **SDK startup**

- **OTEP 0256 (entities data model)** — inchangé sur le fond. Toujours :
  snapshot d'état complet, **pas de champ change-type**, `EntityDelete` non
  garanti → liveness par **absence + `Interval`**. Le repo `oteps` est désormais
  marqué « moved to the Specification repository, preserved for reference ». Le
  travail vivant est donc **dans le repo spec**, plus dans `oteps`. À noter pour
  ADR 0015 : notre phrase « on suit `open-telemetry/oteps` » est partiellement
  périmée — le centre de gravité est `opentelemetry-specification`.
  Les open questions différées sont confirmées : **multiple observers**,
  **identifier selection**, **type-as-identity**, **on-the-wire representation**,
  **liveness**.

- **Spec PR #4836 (entity events spec, dmitryax)** — **MERGÉ le 2026-06-04**
  (merge queue, commit `d42d1af`), approbations jsuereth, tigrannajaryan,
  ChrsMark, langrp. *(État antérieur dans la journée : approuvé non mergé — d'où
  le corps de paragraphe ci-dessous, conservé pour trace mais périmé sur le point
  du merge.)* Confirmé et désormais **dans le main** : relationships **embarquées**,
  descripteur **minimal** `{ relationship.type, entity.type (cible),
  entity.id (cible) }`, source **implicite** (entité émettrice), **attributs
  d'arête retirés**, **pas de delete de relation explicite** (retrait = réémission
  de l'état sans le descripteur, « removed by absence »). La spec porte une section
  **« state changes vs periodic reports »** (cadence / volume) — adjacent à notre
  couche temporelle.

  **Addendum (faits du merge, vérifiés ce jour) :**
  - Fichier de spec = `specification/entities/entity-events.md` (357 lignes), lié
    depuis `entities/README.md`. Statut « Development » (experimental).
  - `entity.id` resserré de `map<string,AnyValue>` à **`map<string,string>`**
    (simplification). Plus restrictif que notre boundary (scalaires) → pas de
    rupture d'ingestion, à tracer dans `otel-mapping.md`.
  - `report.interval` renommé `entity.report.interval`, en **secondes** (notre
    `otel.entity.interval` est en **ms** → divergence d'unité à réconcilier dans
    la couche de migration).
  - **« Patch event » nommé dans le texte mergé** : « the specification may
    introduce a 'patch' event mechanism to communicate only the changes rather
    than the full state. » C'est une **porte d'accroche concrète et nouvelle**
    pour notre couche de changement temporel.
  - **Liveness officielle = notre sweeper** : `entity.delete` existe mais « not
    guaranteed » ; backstop = `entity.report.interval`, le consommateur considère
    l'entité disparue si l'état n'arrive pas à temps. La spec **décrit littéralement**
    le sweeper d'intervalle de Toise.
  - **Pas de change-type / lifecycle** : seul `EventName` (`entity.state` /
    `entity.delete`) distingue. Le changement reste inféré par diff. **Trou central
    intact.** **Multiple observers toujours non traité.**

- **NOUVEAU — PR #5067 « [Entities] Define identity scope » (dmitryax, ouvert,
  stale au 30 mai, reviewer jsuereth)** — adresse l'open question **identifier
  selection**. Introduit la distinction **identité globale** (ex. `k8s.pod.uid`)
  vs **identité locale** (unique seulement dans une entité-contexte : ex.
  `k8s.container.name` dans un pod, **`process.pid` dans un host**), via un champ
  « ID Context Type », plus un « Global Identity Composition algorithm ».
  **Tension active** : jsuereth veut éviter la logique de détection codée en dur
  (comment un détecteur de process sait-il s'il tourne sur un host ou un
  conteneur sans config explicite ?). thompson-tomo propose des `context_keys`.
  **C'est directement notre terrain** (ADR 0017/0018 : `process.pid` +
  `process.creation.time`, précédence de discriminants).

- **NOUVEAU — PR #5057 « [entities] SDK startup specification » (dyladan, ouvert,
  stale, reviewer jsuereth)** — définit le comportement SDK de détection
  d'entités : `Detect` renvoie ≥1 entité ; les **identifying attributes MUST NOT
  change** pendant la vie de l'entité (≥1 requis) ; les descriptive attributes
  fusionnent dans la Resource au fil de l'eau. **Tension active** : détection
  synchrone des identifying attributes (argument JS / pipeline déterministe de
  dyladan) vs détecteurs qui font des appels réseau (metadata server GCP,
  objection jsuereth). Moins central pour Toise (on **ne collecte pas**), mais
  l'invariant « identifying attributes immuables » **valide notre ADR 0018**
  (exact/immutable identity) — c'est une convergence à noter.

- **Roadmap / project board `.project#16`** — **org-interne, 404 en accès
  anonyme**. À récupérer par le mainteneur connecté (ou via `gh` authentifié).

- **PR #4956 (Prometheus job/instance)** — **ouvert**, dernière activité
  2026-06-04 (approuvé ArthurSens, jack-berg ; commentaires cyrille-leclerc).
  Préserve `job`/`instance` comme **attributs identifiants** en round-trip
  Prom↔OTLP (défaut `service.name`←`job`, `service.instance.id`←`instance`).
  Plus pertinent identité qu'anticipé, mais reste **tangentiel** pour Toise (pas
  de relations, pas de temporel).

### Lecture d'ensemble (révisée après le merge de #4836)

Correction de lecture : le SIG **n'est pas qu'en décantation** — il vient de
**livrer** le socle état+relations (#4836 mergé aujourd'hui). Il consolide en
parallèle **l'identité** (#5067) et la **détection/SDK** (#5057), encore stale.
Ce qui reste **non écrit** : le **lifecycle temporel / change-type** et le
« entity signal tracking changes over time ». Notre cœur est donc, plus que jamais,
le trou non comblé — et le « patch event » nommé dans #4836 en est le marqueur
upstream explicite. Posture recommandée inchangée : **écoute d'abord**, antériorité
ciblée sur l'identité, temporel différé jusqu'à l'ouverture du chantier.

---

## 2. Logistique du SIG (établie)

- **Nom** : OpenTelemetry **Entities SIG** (alias « Resources and Entities »).
- **Réunion** : **tous les lundis 09:00 PT**, hebdomadaire (Zoom).
- **Notes de réunion** : Google Doc
  `docs.google.com/document/d/15Yt9ss2_EhuFPqItPbk4vjfpeRDAQ5WCUVuY_kCeOAo`
  — **non lisible via fetch anonyme**. À ouvrir connecté pour le pouls réel
  (dates récentes, qui parle, sujets chauds). **Action mainteneur.**
- **Slack** : **#otel-entities** sur CNCF Slack (archive `C06QEG97W7L`).
- **Calendrier** : s'abonner via `calendar-entities@opentelemetry.io` (groupe
  Google) ou `calendar-all@opentelemetry.io` pour tout. Vue web : calendrier
  communautaire OTel.

### Gouvernance / process OTEP

- L'OTEP est le véhicule pour les changements **cross-cutting** (nouveau
  comportement, multi-langages). Fork → `0000-template.md` → PR → renommer l'ID
  au numéro de PR.
- **Approbation : 4 reviewers github-approve l'OTEP → merge.** Puis une issue
  d'intégration spec est créée ; **4 approbations** également pour merger le PR
  spec, qui est ensuite versionné.
- Implication pour nous : un OTEP « entity change events » exigerait de
  **convaincre 4 approbateurs** parmi le noyau (dmitryax, jsuereth,
  tigrannajaryan, ChrsMark, langrp). D'où le **jeu long** : crédibilité
  d'abord.

---

## 3. Impact pour Toise

- **Notre cœur reste le trou central et non disputé.** Aucune des PRs en vol
  (#5067 identité, #5057 SDK) ne touche au **lifecycle temporel / change-type**.
  La taxonomie de changement (ADR 0006) + la bi-temporalité (ADR 0005) +
  liveness (ADR 0019) restent notre antériorité différenciante. Le « entity
  signal » futur est toujours vacant.

- **#5067 est l'occasion d'antériorité la plus immédiate.** Le débat
  « identité locale vs globale » + « comment un détecteur sait son contexte »
  est *exactement* ce que nos ADR 0017 (identity & stability) et 0018 (exact
  matching) ont tranché en production : `process.pid` est local au host,
  identifiant seulement **apparié à `process.creation.time`**, et nous avons une
  **précédence de discriminants** (`serial:<PEN>` via `sysObjectID`, etc.). C'est
  un *prior art* crédible et **humble** — on a buté sur le même problème, voici
  comment on l'a résolu, pas « faites comme nous ».

- **#5057 valide silencieusement ADR 0018.** L'invariant upstream « identifying
  attributes MUST NOT change » = notre identité exacte/immuable. Bon signe
  d'alignement ; pas une cible de prise de parole (on ne fait pas de SDK/collecte).

- **Cohérence interne à surveiller (non-upstream).** L'ADR 0006 liste encore
  `relation.attribute_changed`, alors que l'ADR 0022 (04 juin) retire les
  attributs d'arête (promotion attribut→entité). Ce n'est pas un sujet SIG, mais
  si on cite notre taxonomie upstream un jour, il faudra une version réconciliée
  pour ne pas exposer une incohérence. **À signaler au mainteneur**, hors lane.

- **Dettes de boundary à tracer (interne, non-upstream) suite au merge de #4836.**
  `docs/data-model/otel-mapping.md` qualifie encore #4836 de « approved-not-merged »
  (lignes 103, 326) — **périmé**. À corriger, et à tracer : `entity.id` resserré à
  `map<string,string>` upstream (on accepte plus large, pas de rupture) ;
  `entity.report.interval` en **secondes** vs notre `otel.entity.interval` en **ms**.
  Modifs internes, soumises au workflow git du mainteneur (branche `docs/...`, PR).

- **#4836 (désormais mergé) conditionne notre blog.** La refonte de la section
  relations du billet (opentelemetry.io#10124) et les réponses à jsuereth sont
  **en attente de la décision Toise #65** (embarqué vs séparé). ADR 0022 a
  *déjà* tranché côté moteur (**embarqué**, arêtes nues, `entity.relation.*`
  déprécié en shim). Donc côté contenu, notre position est arrêtée — mais
  **consigne explicite : ne rien rédiger pour le blog ni upstream pour l'instant**.
  Statut : en attente.

---

## 4. Recommandation (angle d'intervention)

**Phase d'écoute, pas de prise de parole.** Trois raisons : (a) le SIG est en
décantation (PRs stale / approuvées-non-mergées) ; (b) notre crédibilité
upstream est à zéro (le seul contact est la review blog de jsuereth) ; (c) le
blog lui-même est gelé en attente de #65.

**Angle long recommandé** : entrer par l'**identité (#5067)**, pas par le
temporel. Raisonnement :
1. #5067 est **vivant et disputé maintenant** ; le temporel est **non écrit** —
   on ne peut pas commenter ce qui n'existe pas.
2. L'identité est un terrain où on a un *prior art* **factuel, borné, humble**
   (un cas concret : process local au host, pid+creation.time). C'est le bon
   premier contact : utile, pas surdimensionné.
3. Cela construit la crédibilité qui rendra audible, **plus tard**, notre
   proposition sur le lifecycle temporel (le grand objectif), quand le SIG
   ouvrira ce chantier.

**Le grand objectif (couche de changement temporel) reste différé** jusqu'à ce
que (a) on ait un historique de contributions utiles et (b) le SIG signale
l'ouverture du « entity signal ». Véhicule probable à terme : **co-proposition
d'un OTEP « entity change events »** (4 approbations requises) ou contribution
directe au futur entity signal — à décider quand la fenêtre s'ouvre.

---

## 5. Action proposée (à valider par le mainteneur)

Par ordre :

1. **[Mainteneur] Débloquer la visibilité interne.** Ouvrir, connecté : (a) le
   Google Doc des notes de réunion (pouls réel du SIG, dates récentes) ; (b) le
   project board `.project#16`. Me remonter un dump ou un résumé — je ne peux pas
   y accéder. Ça conditionne la précision des prochains digests.

2. **[Liaison, sans publier] Préparer un dossier d'antériorité « identité »**
   adossé à #5067 : cartographier nos ADR 0017/0018 contre la distinction
   locale/globale et le débat « context detection » de jsuereth. Livrable = une
   note interne (pas un commentaire upstream) pour décider *si* et *comment* on
   apporte ce prior art. **Ne rien poster.**

3. **[Liaison] Assister à 1-2 réunions du lundi en lecture seule** (via le
   mainteneur ou un compte) avant toute parole, pour calibrer le ton et savoir
   qui pousse quoi.

4. **[Mainteneur, interne] Corriger `otel-mapping.md`** : mentions
   « approved-not-merged » → « merged 2026-06-04 (d42d1af) » + tracer les dettes
   unité (secondes vs ms) et `id` string. Et mettre à jour l'issue Toise #66 avec
   le fait « #4836 mergé » (contenu préparé par moi, posté par toi).

5. **[Gelé] Blog opentelemetry.io#10124** : aucune rédaction tant que #65 n'est
   pas acté côté process Toise (rappel : ADR 0022 a tranché « embarqué » côté
   moteur ; reste la décision de réponse au reviewer).

**Rien de tout ceci n'implique de poster upstream.** Tout reste en brouillon
jusqu'à accord explicite.

---

## Liens

- OTEP 0256 : https://github.com/open-telemetry/oteps/blob/main/text/entities/0256-entities-data-model.md
- Spec PR #4836 : https://github.com/open-telemetry/opentelemetry-specification/pull/4836
- PR #5067 (identity scope) : https://github.com/open-telemetry/opentelemetry-specification/pull/5067
- PR #5057 (SDK startup) : https://github.com/open-telemetry/opentelemetry-specification/pull/5057
- PR #4956 (Prometheus job/instance) : https://github.com/open-telemetry/opentelemetry-specification/pull/4956
- Fichier mergé `entity-events.md` : https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/entities/entity-events.md
- OTEP process : https://github.com/open-telemetry/opentelemetry-specification/blob/main/oteps/README.md
- Community repo : https://github.com/open-telemetry/community
- Notes SIG (accès connecté) : https://docs.google.com/document/d/15Yt9ss2_EhuFPqItPbk4vjfpeRDAQ5WCUVuY_kCeOAo
- Slack #otel-entities : https://cloud-native.slack.com/archives/C06QEG97W7L
- Project board (org-interne) : https://github.com/orgs/open-telemetry/projects/16
