# Veille — vocabulaire réseau Toise vs OpenTelemetry (état au 2026-09-29)

Statut : brief interne. Rien n'est publié. Destiné au mainteneur (Matthieu).

Question posée : sommes-nous « dans les clous » avant d'ajouter une entité identifiée
par adresse MAC, et le cadre tient-il pour un vrai réseau d'entreprise (OSPF, BGP,
SD-WAN) et pas seulement pour de l'adjacence L2 ?

Réponse courte : **oui pour l'état, non pour le nommage, et le paysage a changé sous
nos pieds le 2026-07-29.** Le détail ci-dessous.

---

## 0. Le changement de contexte qu'il faut intégrer d'abord

Le **Network Observability project est charté** : community PR
[#3560](https://github.com/open-telemetry/community/pull/3560) **mergée le 2026-07-29**,
fichier [`projects/network.md`](https://raw.githubusercontent.com/open-telemetry/community/main/projects/network.md).

- Leads : Rob Cowart, Sven Cowart, Antonio Jimenez (@ajimenez1503), Braydon Kains (@braydonk).
- Sponsor TC : Liudmila Molkova (@lmolkova). Liaison GC : Ted Young.
- Durée annoncée : 18 mois. Mois 1-2 = near-term, 3-6 = mid-term, 7-18 = long-term.
- **Matthieu est listé comme contributeur** dans la proposition. Toute prise de parole
  ici est de l'input d'insider, pas de la contribution à froid.
- Board : `open-telemetry/projects/202` (« Network Semantic Conventions and
  Instrumentation libraries »). Labels : `area:network`, `area:source`,
  `area:destination`, `area:dns`.
- L'issue [#3769](https://github.com/open-telemetry/semantic-conventions/issues/3769)
  « New Working Group: Networking » (braydonk, 2026-06-04) est **fermée** — absorbée par
  le projet.

Le projet **nomme explicitement les entités réseau comme livrable mid-term** (donc
fenêtre ≈ 2026-10 → 2027-01), avec une taxonomie par couche, citation du fichier :

> Layer 2: network interface, OSPF interface, 802.1d bridge, 802.1d bridge port,
> spanning tree; Layer 3: IP subnet, BGP, BGP peer, OSPF, OSPF area, OSPF neighbor;
> Layer 4: service access point (SAP — layer-4 network session/socket), TCP, UDP,
> session, flow

Long-term : « stabilize core network attributes and **drive entity definitions toward
stability** ».

**Conséquence directe sur notre antériorité** : notre note interne
`docs/outreach/sig/draft-discussion-network-entities.md` (rédigée le 2026-06-08) repose
sur la prémisse « aucun travail réseau-entités en vol, c'est un trou vide ». Cette
prémisse est **fausse depuis le 2026-07-29**. Ce brouillon est à réécrire avant tout
usage : il doit passer de « interest check, est-ce que quelqu'un s'y intéresse ? » à
« input sur un livrable nommé d'un projet charté ». Le fond technique reste bon, le
cadrage ne l'est plus.

---

## 1. MAC — existe-t-il une clé semconv ?

### Stable : rien.

Aucune clé MAC n'est stable. Le namespace `network.*` stable ne contient que
`network.local.address/port`, `network.peer.address/port`, `network.protocol.name/version`,
`network.transport`, `network.type`
([registry network](https://opentelemetry.io/docs/specs/semconv/registry/attributes/network/)).

### Development : deux clés existent, aucune n'est ce dont nous avons besoin.

| Clé | Stabilité | Sens exact | Pourquoi ça ne suffit pas |
| --- | --- | --- | --- |
| `host.mac` | Development | **tableau** des MAC d'un hôte, hors loopback | attribut descriptif d'hôte, pas par interface, pas identité |
| `hw.network.physical_address` | Development | « Physical address of the adapter (e.g. MAC address, or WWNN) » | c'est le plus proche d'une MAC par port, mais porté par le modèle d'inventaire matériel `hw.*` |

`host.mac` fixe en revanche **le format**, et c'est la partie qui compte :

> MAC Addresses MUST be represented in IEEE RA hexadecimal form: as hyphen-separated
> octets in uppercase hexadecimal form.

Exemple normatif : `AC-DE-48-23-45-67`
([registry host](https://opentelemetry.io/docs/specs/semconv/registry/attributes/host/)).

Le modèle `hw.*` ([hardware/network](https://opentelemetry.io/docs/specs/semconv/hardware/network/))
vient avec `hw.id` (« unique **within the monitored host** »), `hw.type=network`,
`hw.parent`, `hw.serial_number`, plus `hw.network.bandwidth.limit` (= notre `speed`),
`hw.network.up` (= notre `oper_state`), `hw.network.logical_addresses`. C'est un modèle
d'inventaire **scopé hôte**, pensé pour un agent qui inspecte la machine sur laquelle il
tourne — il ne transpose pas à un port de commutateur vu en SNMP depuis l'extérieur.

### En cours : PR #4146, ouverte, très fraîche.

[PR #4146 « Add MAC address attributes for network endpoints »](https://github.com/open-telemetry/semantic-conventions/pull/4146),
auteur **ajimenez1503** (un des 4 leads du projet Network), **ouverte**, mise à jour
2026-09-29, **0 approbation sur 2 requises**, en attente de review de braydonk et
svencowart. Corrige l'issue #2416 (2025).

Propose six clés, toutes **Development** et **opt-in**, format aligné sur `host.mac` :
`client.mac`, `server.mac`, `source.mac`, `destination.mac`, `network.local.mac`,
`network.peer.mac`. Avertissement PII ajouté après review Copilot.

Elle fait partie d'une série de trois PRs du même auteur, toutes ouvertes, qui sont les
briques d'attributs réseau en train d'atterrir :

- [#4144](https://github.com/open-telemetry/semantic-conventions/pull/4144) CIDR —
  `client/server/source/destination/network.local/network.peer.cidr`, notation CIDR
  canonique bits d'hôte à zéro (maj. 2026-09-29).
- [#4146](https://github.com/open-telemetry/semantic-conventions/pull/4146) MAC (ci-dessus).
- [#4115](https://github.com/open-telemetry/semantic-conventions/pull/4115) numéros de
  système autonome (maj. 2026-09-28), issues #3740 / #1663.

### N'existe pas du tout

Aucune clé pour : la MAC **d'une interface** en tant que telle, une table FDB / bridge
MIB, une MAC comme **clé d'identité**, une MAC apprise sur un port.

**Verdict Q1.** Le **format** est décidé et il faut l'adopter tel quel (majuscules,
octets séparés par tirets). Le **nom de clé** pour « la MAC de cette interface » n'existe
pas et n'est pas en cours : les six clés de #4146 sont toutes orientées flux/endpoint
(client/server/source/destination/local/peer), pas inventaire. Nous devons inventer —
mais inventer le nom, pas le format.

---

## 2. Interfaces réseau — quelle est la forme recommandée aujourd'hui ?

**`network.interface.name` est en Release Candidate**, promu par
[PR #3953](https://github.com/open-telemetry/semantic-conventions/pull/3953) (ChrsMark,
mergée, semconv **v1.44.0 du 2026-08-04**) ; attribut introduit à l'origine par
[PR #1492](https://github.com/open-telemetry/semantic-conventions/pull/1492) (ChrsMark,
2025-12). Exemples normatifs : `lo`, `eth0`.

C'est **la forme recommandée aujourd'hui**, et RC signifie qu'elle est à un cran du
stable : le nom ne bougera plus.

Sur la question `system.device` : la migration est **faite mais pas terminée
proprement**. Les métriques `system.network.io` portent bien `network.interface.name`
(RC) + `network.io.direction` (RC) ; `system.device` subsiste sur certaines métriques
(`system.network.packet.count`) sans note de dépréciation formelle. Incohérence résiduelle
côté semconv, pas une ambiguïté pour nous : **pour désigner une interface, c'est
`network.interface.name`.**

Ce qui n'existe pas : **aucune entité interface**. Le
[registry d'entités](https://opentelemetry.io/docs/specs/semconv/registry/entities/)
compte 3 stables (`service`, `service.instance`, `service.namespace` — plus
`telemetry.sdk`/`telemetry.distro`), 5 RC (`process`, `cicd.*`, `vcs.repository`), 57 en
Development, et **zéro entité `network.*`**. La page précise encore :
« Relationships and signal associations are a work in progress. »

« network interface » figure en tête de la liste L2 du projet Network — donc l'entité
viendra, et il est très probable qu'elle s'appelle `network.interface`.

**Impact Toise immédiat et bon marché.** Nos attributs d'interface sont tous non
namespacés : `mac`, `mtu`, `speed`, `duplex`, `oper_state`, `interface.type`. Ce n'est pas
une collision, c'est une non-conformité de forme (semconv namespace tout). Le seul qui a
un équivalent quasi-stable est `interface.name` → **`network.interface.name`**. C'est le
renommage à faire en priorité : gratuit, RC, et il nous met dans les clous sur le seul
point où l'upstream a déjà tranché.

Nos **deux schémas d'identité disjoints** (`(host.id, interface.name)` côté agent vs
`(interface.name, network.device.id)` côté sonde SNMP) n'ont aucune réponse upstream. Le
seul précédent de forme est `hw.id` « unique within the monitored host » + `hw.parent`,
qui est scopé hôte et ne couvre donc que la moitié de notre cas. C'est la question
« multiple observers » de l'OTEP 0256, toujours ouverte, et c'est notre antériorité.

---

## 3. Équipements réseau, liens, adjacence, topologie

### Côté SIG Resources & Entities : rien, et rien en cours.

- Registry d'entités : aucune entité réseau (vérifié 2026-09-29).
- Spec entités = `specification/entities/{README,data-model,entity-events,entity-propagation}.md`,
  toutes en **Status: Development**. Dernière release spec **v1.61.0 (2026-09-14)**, sans
  changement entités. Les relations sont dans la spec depuis **v1.58.0 (2026-06-22)** mais
  semconv **ne sait pas encore déclarer de relations**.
- Aucune issue semconv dont le titre contient « network entity » (recherche 2026-09-29).
- Pas de `model/entity/network.yaml`.

Autrement dit : le SIG Entities **ne discute pas** d'équipement réseau, de lien ni
d'adjacence. Le sujet a été **délégué au projet Network**, qui l'a inscrit à son
roadmap mais n'a encore rien écrit.

### Le premier artefact upstream qui nomme LLDP : issue #4098.

[Issue #4098 « Decide the attributes for L2/L3 routing and neighbor telemetry (OSPF, BGP,
LLDP, ARP) »](https://github.com/open-telemetry/semantic-conventions/issues/4098),
auteur **svencowart** (lead du projet), **ouverte le 2026-09-11**, dernière activité
**2026-09-28**, labels `area:network` + `triage:needs-triage`.

Le problème posé : la PR #4045 a fixé la doctrine des trois paires
(`client.*`/`server.*`, `source.*`/`destination.*`, `network.local.*`/`network.peer.*`)
et a **explicitement laissé de côté** la télémétrie de routage et de voisinage L2/L3. Or
`network.local.*`/`network.peer.*` sont définis **en termes de socket**
(`getsockname`/`getpeername`) : BGP sur TCP s'y coule bien, mais OSPF (IP direct), LLDP
(couche liaison) et ARP n'ont aucune sémantique de socket.

Quatre options proposées :

1. réutiliser les attributs socket existants ;
2. introduire `network.neighbor.local.*` / `network.neighbor.peer.*` ;
3. créer une famille `network.routing.*` ;
4. hybride — réutiliser pour les protocoles socket, nouvelle famille pour les autres.

**C'est un débat purement attributaire.** Aucune des quatre options ne pose la question
entité/relation. Aucun commentaire n'était lisible sur la page au 2026-09-29 (le rendu ne
montrait que le corps de l'issue) — il peut donc y avoir des positions déjà prises que je
n'ai pas vues.

C'est, à mon avis, **le point d'entrée le plus rentable dont nous disposons aujourd'hui** :
frais (18 jours), ouvert, non trié, écrit par un lead, et il décide implicitement si
l'adjacence upstream sera une **paire d'attributs plate sur un flux** ou une **arête d'un
graphe traversable**. Si l'issue se règle en « option 2, paire d'attributs », la topologie
réseau OTel n'aura pas de forme graphe, et notre modèle divergera durablement sans que
personne n'ait jamais posé la question.

### FDB / bridge : nommé, non spécifié.

« 802.1d bridge » et « 802.1d bridge port » sont sur la liste L2 du projet — donc le
concept exact dont vous avez besoin pour la jointure FDB **est prévu**, sous forme
d'entités, sans une ligne d'écrite. C'est le meilleur moment possible pour apporter de
l'antériorité, et le pire pour figer un nom de notre côté.

### SNMP : [issue #2399](https://github.com/open-telemetry/semantic-conventions/issues/2399)

« Support snmp in the network namespace », thompson-tomo, ouverte 2025-06-20, **toujours
ouverte, toujours `triage:needs-triage`** au 2026-09-28, mais désormais **assignée à
robcowart** et rattachée au board du projet Network (statut « Triage »). Contenu quasi
vide (une demande + un renvoi à RFC1213-MIB), zéro commentaire, **aucune discussion
d'identité** (pas de sysObjectID, pas de serial, pas d'ifIndex). Le trou d'identité SNMP
est intact.

---

## 4. Routage et protocoles dynamiques (OSPF, BGP, VRF, SD-WAN)

### Existe aujourd'hui : rien.

Zéro attribut de route, de next-hop, de table de routage, de VRF, de MPLS, de SD-WAN.
Zéro entité. Le namespace `network.*` n'a pas une seule clé de routage.

### En cours : embryonnaire, et uniquement au niveau attribut.

`projects/network.md` liste en mid-term :

- « Develop **BGP telemetry** specifications addressing ASN, route updates, and metrics »
- « Create standards for **traceroute** telemetry including hop data and latency measurements »
- entités L3 : « IP subnet, BGP, BGP peer, OSPF, OSPF area, OSPF neighbor »

Et les PRs #4115 (AS number/name), #4144 (CIDR) sont les premières briques concrètes —
toutes deux **ouvertes**, aucune mergée.

`network.peer.as.number` / `.as.name` (#3740, #1663) et `network.peer.address.prefix`
(#3731) sont les issues d'origine, toutes ouvertes, dernière activité 2026-09-14/09-21.

### N'existe nulle part, ni en roadmap : VRF, SD-WAN, MPLS.

Pas une mention. Si Toise modélise du VRF ou du SD-WAN, c'est du terrain vierge intégral,
sans horizon de convergence.

### Prior art hors OTel : OpenConfig / gNMI.

C'est le schéma de facto de l'industrie réseau : `openconfig-interfaces`,
`openconfig-network-instance` (qui est le modèle VRF), `openconfig-bgp`,
`openconfig-ospfv2`, `openconfig-lldp`. Côté OTel, le seul pont public est
[`openconfig/clio`](https://github.com/openconfig/clio), décrit comme « OpenTelemetry to
gNMI Bridge » — c'est-à-dire un collecteur OTel qui **expose** sa télémétrie en gNMI,
soit **l'inverse** du sens qui nous intéresse. **Aucun travail de mapping sémantique
OTel ↔ OpenConfig n'existe dans semconv.**

Observation à verser au débat, et je la donne comme **mon inférence, pas comme une
position upstream** : OpenConfig est un arbre de configuration+état (chemins, listes
clefées), pas un modèle d'entités/événements. La correspondance naturelle n'est pas
« métrique ↔ métrique » mais **« clé de liste OpenConfig ↔ clé d'identité d'entité »** :
`/interfaces/interface[name=...]`, `/network-instances/network-instance[name=...]`,
`/bgp/neighbors/neighbor[neighbor-address=...]`. Chaque `[clé=...]` est littéralement un
jeu d'attributs identifiants au sens de l'OTEP 0256. C'est, je crois, l'argument le plus
utile que nous puissions apporter à la question « comment identifier une entité réseau »,
et il ne nous coûte aucune prétention : il consiste à dire « l'industrie a déjà choisi ses
clés, reprenons-les » plutôt que « voici les nôtres ».

---

## 5. Risque de collision, nom par nom

Rappel du cadre : `network`, `source`, `destination`, `dns` sont désormais des namespaces
**avec un propriétaire** (le projet Network, sponsor TC lmolkova) et un flux de PRs actif
qui y plante des clés. Ce n'est plus un espace vide.

| Nom Toise | Collision de nom | Collision de sens | Rupture si convergence |
| --- | --- | --- | --- |
| `network.device` (type) | aucune aujourd'hui | — | **Moyenne-haute.** Rien n'occupe le nom, mais rien ne le protège non plus, et l'upstream le choisira probablement avec **une autre identité** que notre échelle `serial:<PEN>` > `engine:` > `mac:` > `name:` > `mgmt:`. Une identité qui change = empreinte d'identité qui change = re-keyage de tout l'historique. |
| `network.device.id` (clé) | aucune | — | **Haute.** C'est une clé d'attribut plantée dans un namespace appartenant à autrui. |
| `network.interface` (type) | aucune | — | **Haute mais favorable.** L'upstream prendra très probablement ce nom exact (liste L2). Le risque n'est pas le nom, c'est **l'identité** : s'il scope par équipement et que notre agent scope par hôte, nos deux schémas disjoints deviennent un seul, exigeant un re-keyage ou une surcouche `same_as` (ADR 0020). |
| `interface.name`, `mac`, `mtu`, `speed`, `duplex`, `oper_state`, `interface.type` | aucune | — | **Faible techniquement, certaine formellement.** Clés non namespacées = non conformes par construction. `interface.name` a un remplaçant RC (`network.interface.name`) ; `speed`/`oper_state` ont des équivalents Development (`hw.network.bandwidth.limit`, `hw.network.up`) ; `mac` a un format normatif (`host.mac`) mais pas de nom. |
| `network.address` (type, identité = l'IP seule) | aucune | **oui** | **La plus risquée sémantiquement.** `network.local.address` / `network.peer.address` sont **stables** et signifient « adresse de socket » ; #4144 et #4115 étendent cette famille. Un lecteur venant d'OTel lira notre type de travers. Et une identité = IP nue est l'anti-pattern `172.17.0.1` que l'ADR 0034 nomme déjà, et que l'ADR 0032 a corrigé pour les endpoints loopback en ajoutant une 4e clé. Enfin « IP subnet » est sur la liste L3 upstream et recoupe partiellement le concept. |
| `network.segment` | aucune | partielle | **Faible.** Upstream dit « IP subnet » (L3) et « 802.1d bridge » (L2) ; notre segment est un domaine de broadcast, donc plus proche du second. Notre ADR 0034 a déjà eu l'honnêteté de laisser `vlan:` et `k8s:` ouverts faute de scope d'identité — position tenable. |
| `network.route`, `network.endpoint` | aucune | — | **Faible.** Aucun équivalent upstream. `network.endpoint` est le mieux aligné du lot : son identité est bâtie sur `server.address` + `server.port` + `network.transport`, **trois attributs stables**. C'est le modèle à imiter. |
| `connected_to`, `has_interface`, `bound_to`, `has_route`, `next_hop_via` | **aucune** | — | **Nulle aujourd'hui, ouverte demain.** semconv ne définit **aucun type de relation** ; `depends_on` n'est qu'un exemple de la spec. Les types de relation sont un enum ouvert. Le vrai risque n'est pas un conflit de nom mais le scénario #4098-option-2 : si l'upstream décide que l'adjacence est une **paire d'attributs** (`network.neighbor.*`) et non une relation, notre `connected_to` cesse d'être le porteur canonique du concept. |

**Les trois ruptures concrètes à avoir en tête**, par gravité décroissante :

1. Un changement de **clé ou de format de valeur d'identité** → empreinte d'identité
   différente → discontinuité d'historique sur un graphe append-only. C'est la seule
   rupture réellement coûteuse. Corollaire pratique : **conformer les formats est urgent,
   conformer les noms de clés ne l'est pas.**
2. L'upstream fixe pour `network.interface` une identité incompatible avec l'un de nos
   deux schémas → réconciliation obligatoire, très probablement en surcouche `same_as`.
3. L'upstream choisit l'adjacence-comme-attribut → notre graphe reste traversable, le
   leur ne l'est pas, et l'interopérabilité devient une traduction et non un mapping.

---

## 6. Recommandation

### 6.1 La frontière « réutiliser semconv » vs « assumer une extension Toise »

Je propose trois niveaux, avec une règle de tranchage unique : **on se conforme d'autant
plus fort que la chose est difficile à changer plus tard.**

**Niveau A — reprendre à l'identique, sans exception : tout ce qui est Stable ou RC.**
`network.interface.name` (RC), `network.transport` / `server.address` / `server.port`
(stables, déjà utilisés par `network.endpoint`), `host.id`, et le **format** MAC de
`host.mac`. Coût nul, conformité gratuite, et ce sont les seuls points où l'upstream a
réellement tranché.

**Niveau B — reprendre la *forme*, garder notre clé : le Development semconv.**
`hw.network.physical_address`, `hw.network.bandwidth.limit`, `hw.network.up` décrivent nos
`mac`, `speed`, `oper_state`. Mon avis : **ne pas adopter les clés `hw.*`**. Elles
viennent avec un modèle d'inventaire matériel scopé hôte (`hw.id` « unique within the
monitored host », `hw.parent`) qui ne transpose pas à un port de commutateur observé en
SNMP depuis l'extérieur. En revanche **adopter les formats de valeur**, et le format MAC
en particulier, parce qu'un format entre dans le hachage d'identité et qu'un nom de clé
n'y entre pas.

**Niveau C — assumer l'extension, sous un préfixe que nous possédons.** Tout le reste :
l'identité par MAC, la topologie, le routage, le next-hop, le segment.

### 6.2 La discipline de préfixe — ma recommandation nette

**Arrêter de créer de nouvelles clés d'attribut à l'intérieur de `network.*`.** Ce
namespace a un propriétaire depuis le 2026-07-29 et trois PRs actives qui y plantent des
clés. Chaque clé `network.<x>` que nous inventons est un conflit futur que nous perdrons,
parce que nous ne sommes pas l'autorité de nommage.

Concrètement, et ce sont des recommandations à la lane dev, pas des décisions :

- **Ne rien renommer aujourd'hui côté *types d'entité*.** `network.device`,
  `network.interface`, `network.segment`, `network.route`, `network.address`,
  `network.endpoint` restent. Le registry d'entités semconv n'occupe pas encore cet
  espace, le renommage re-keyerait un graphe de production, et le bénéfice serait nul
  tant que l'upstream n'a rien publié.
- **Renommer une seule chose** : `interface.name` → `network.interface.name`. C'est du RC,
  ça ne bougera plus, et c'est le seul endroit où nous sommes en retard sur une décision
  déjà prise.
- **Toutes les *nouvelles* clés d'attribut sous un préfixe Toise.** Le projet a déjà le
  précédent : le vocabulaire de gouvernance transverse (owner / criticality / location /
  lifecycle) a été mis sous `entity.*` plutôt que d'aller squatter des namespaces
  upstream. Même règle ici.
- **Écrire la déclaration de transitionnalité** dans `docs/data-model/otel-mapping.md` :
  les noms de types `network.*` sont transitionnels et seront revus quand le projet
  Network publiera son registry. Une divergence déclarée est une position ; une divergence
  silencieuse est une dette.

### 6.3 Sur l'entité identifiée par MAC — la question qui a déclenché la demande

Je recommande de **ne pas créer un type d'entité dont l'identité est une MAC nue**, et
l'argument ne vient pas de l'upstream mais de nos propres ADR.

Une MAC n'est pas globalement unique en pratique : adresses localement administrées,
MAC virtuelles VRRP/HSRP partagées entre deux châssis, MAC identiques sur des bridges
isolés, randomisation côté clients. Une identité = MAC nue est exactement l'anti-pattern
`172.17.0.1` que l'ADR 0034 nomme pour `vlan:` et que l'ADR 0032 a corrigé pour les
endpoints loopback en ajoutant une clé de scope. Ce serait **le deuxième** type à
identité non scopée après `network.address` — donc l'aggravation d'un problème connu,
pas une nouveauté.

Et surtout : **la jointure visée n'a pas besoin de ce type.** Ce que donne la FDB, c'est
`(port de bridge, MAC apprise)`. Ce que donne l'agent machine, c'est
`(host.id, interface.name, MAC)`. La jointure recherchée est l'énoncé « ces deux objets
sont la même interface » — or **c'est une corrélation, pas un fait asserté**. Sous le
principe fondateur (ADR 0022, le moteur ne stocke que l'asserté), le moteur devrait
stocker : l'entité port-de-bridge telle que la sonde SNMP l'assère, l'entité interface
telle que l'agent l'assère, et la MAC comme **attribut identifiant** de chacune — et
laisser la jointure à la surcouche `same_as` pondérée (ADR 0020, livrée en v0.8.0), au
read-time. Créer un nœud MAC partagé, c'est **matérialiser une déduction dans la source de
vérité**, ce que le moteur s'interdit.

Si un nœud de rendez-vous s'avérait malgré tout nécessaire, la version honnête serait une
identité **scopée par domaine de broadcast** (`mac:<addr>` dans `network.segment:<id>`) —
et elle serait alors bloquée sur exactement la question de scope que l'ADR 0034 a laissée
ouverte pour `vlan:`. Autant le dire franchement plutôt que de trancher à l'aveugle.

Je souligne que c'est une lecture de liaison spec, pas un arbitrage de conception : la
décision est à la lane dev.

### 6.4 Le véhicule upstream — par ordre de rendement

**1. Commenter l'issue #4098. C'est l'action à mener, et elle est datée.**

Pourquoi elle et pourquoi maintenant : ouverte il y a 18 jours, toujours `needs-triage`,
écrite par un lead du projet, et elle arbitre implicitement la question qui décide de
toute la suite — adjacence comme attribut ou comme arête. Une fois les quatre options
tranchées, ce sera acquis pour des années.

Angle recommandé, en apport d'expérience et non en contre-proposition : les quatre
options sont toutes attributaires ; or la télémétrie de voisinage a aussi une forme
entité/relation, et le choix entre les deux mérite d'être **explicite** plutôt que
subi. Plus le vécu concret : l'interface comme entité, l'adjacence comme arête nue entre
deux interfaces, la provenance (quel observateur, via quel protocole) portée par le scope
d'instrumentation et non par l'arête — ce dernier point étant directement la conséquence
du « pas d'attributs d'arête » de la spec mergée, que nous avons transformé en règle de
conception plutôt qu'en limitation.

Le tout en soulignant que la question de #4098 (LLDP/ARP n'ont pas de socket) est
**exactement** le symptôme d'un modèle attributaire appliqué à une relation.

Matthieu étant contributeur listé, c'est de l'input attendu.

**2. Réécrire `draft-discussion-network-entities.md` avant tout usage.** Sa prémisse est
caduque depuis le 2026-07-29. Nouveau cadrage : input à un livrable nommé (« Establish
network-related entities »), pas interest check. Le one-pager gist reste le lien de fond.

**3. Ne pas ouvrir d'issue « entités réseau » maintenant.** La fenêtre mid-term du projet
(≈ 2026-10 → 2027-01) est celle où ils vont l'ouvrir eux-mêmes. Y arriver avec de
l'antériorité déjà visible via #4098 vaut mieux que de leur préempter leur propre
livrable. Jeu long.

**4. Rapport d'expérience sur l'identité, pas OTEP.** La question d'identité (échelle de
précédence équipement, double schéma d'interface, observateurs multiples) est
précisément ce que ni #2399 ni `projects/network.md` ne traitent, et c'est là que nous
avons des heures de production. Véhicule : une issue semconv `area:network` cadrée
« experience report » — même patron que semconv #4024 déjà déposée. **Pas un OTEP** : le
dépôt `oteps` est archivé, le vocabulaire réseau est du ressort de semconv, et une OTEP
serait un mauvais signal de poids.

**5. Optionnel, faible priorité : une remarque courte sur #4146.** Les six clés proposées
sont toutes orientées flux/endpoint ; un consommateur d'inventaire/topologie a besoin de
la MAC **sur l'interface elle-même**. C'est factuel, petit, non polémique. À faire
seulement si le commentaire #4098 est bien reçu.

### 6.5 Logistique à récupérer — je n'y ai pas accès

`projects/network.md` **ne mentionne ni canal Slack ni cadence de réunion**. Le board
`open-telemetry/projects/202` et les éventuels documents de travail (Google Docs, canal
Slack CNCF) ne me sont pas accessibles. C'est très probablement là que la taxonomie
d'entités est en train d'être ébauchée. Matthieu étant contributeur listé, la question
directe aux leads est le chemin court.

---

## Incertitudes, nommées

- **Aucun commentaire n'était lisible sur #4098** au 2026-09-29 (rendu limité au corps de
  l'issue). Des positions peuvent déjà être prises. À revérifier avant de rédiger quoi
  que ce soit.
- **Board `projects/202`, Slack `#otel-network`, notes de réunion du projet Network :
  non consultés** (non publics ou hors de portée de mes outils). La conclusion « rien
  d'écrit sur les entités réseau » vaut pour les dépôts publics uniquement.
- « L'upstream nommera probablement l'entité `network.interface` » est une **inférence**
  tirée de la prose du roadmap. Aucun YAML, aucune PR ne le confirme.
- Les pages de release GitHub m'ont été rendues avec des années erronées (2024 au lieu de
  2026) ; j'ai recoupé les couples version/date avec l'état connu (spec v1.61.0 =
  2026-09-14, v1.60.0 = 2026-08-07, v1.58.0 = 2026-06-22 ; semconv v1.44.0 = 2026-08-04).
  Le recoupement est cohérent mais mérite une vérification si une date devient
  load-bearing dans un texte publié.
- Je n'ai pas vérifié si `docs/data-model/otel-mapping.md` déclare déjà les types
  `network.*` comme transitionnels — la recommandation 6.2 suppose que non.
- Le statut réel de `system.device` (déprécié ou simplement non migré) n'est pas tranché
  par la documentation ; je le lis comme « migration incomplète », sans note formelle.

## Action proposée (à valider)

1. Décision de la lane dev sur l'entité MAC, à la lumière de 6.3 — **avant** d'écrire
   quoi que ce soit upstream.
2. Si accord sur l'angle : je rédige le brouillon de commentaire pour **#4098** (anglais,
   ton expérience-report, sourcé), livré ici pour validation. Rien n'est posté sans votre
   accord explicite.
3. En parallèle, réécriture de `draft-discussion-network-entities.md` sous le nouveau
   cadrage post-charte.
4. Renommage `interface.name` → `network.interface.name` : à porter comme issue Toise,
   pas par moi.
