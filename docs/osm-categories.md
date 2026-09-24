# OSM-Kategorie-Mapping

`cmd/poi_categories.go` ist der gemeinsame Katalog für die Kategorie-Filter der
Oberfläche, die Ortssuche, die lokale KI-Suche und
`GET /api/v1/geo/pois?category=…`. Er enthält 41 Kategorien mit deutschen und
englischen Aliasen.

## Regeln

- Groß-/Kleinschreibung, Umlaute (auch als `ae`/`oe`/`ue`/`ss`), Akzente,
  Bindestriche und ausdrücklich aufgeführte Pluralformen werden normalisiert.
- In Sätzen gewinnt die längste bekannte Wortfolge, bei Gleichstand die zuerst
  genannte. Teilwörter zählen nicht: `Parkstraße` ist kein Park, `Seestraße`
  kein See und `Zahnarzt` kein allgemeiner Arzt. Eigennamen wie `Isar` sind
  keine Kategorien.
- Eine Kategorie kann mehrere alternative Filter haben (**oder**). Innerhalb
  eines Filters müssen alle Bedingungen zutreffen (**+**). Durch Semikolon
  getrennte Tag-Werte werden einzeln geprüft.
- Die Suchreihenfolge richtet sich weiter nach Namen und Adressen. Exakt
  erkannte Kategorie-Aliase fließen zusätzlich ein. Kategorien werden pro Suche
  nur einmal aufgelöst.
- Die Geo-API akzeptiert auch unbekannte rohe Tag-Werte (z. B. `company`) aus
  allen POI-Schlüsseln. Ein Filter der Form `key=value` wie
  `amenity=hospital` prüft genau diesen Schlüssel und Wert. Er wird nicht um
  Alternativen wie `healthcare=hospital` erweitert.

Beispiele: `category=Krankenhäuser`, `category=charging_station`,
`category=emergency%3Dfire_hydrant`.

## Alle Kategorien

| ID | OSM-Tags | Aliase |
| --- | --- | --- |
| `fuel` | `amenity=fuel` | tankstelle, tankstellen, benzin, diesel, gas station, fuel |
| `station` | `railway=station` | bahnhof, bahnhöfe, train station, railway station |
| `bus_stop` | `highway=bus_stop` | haltestelle, bushaltestelle |
| `pharmacy` | `amenity=pharmacy` oder `healthcare=pharmacy` | apotheke, pharmacy, apotheken, pharmacies |
| `supermarket` | `shop=supermarket` | supermarkt, supermarket, supermärkte, supermarkets |
| `bakery` | `shop=bakery` | bäckerei, bakery, bäckereien, bakeries |
| `parking` | `amenity=parking` | parkplatz, parking, parkplätze, car park |
| `bank` | `amenity=bank` | bank |
| `atm` | `amenity=atm` | geldautomat, atm, geldautomaten |
| `hospital` | `amenity=hospital` oder `healthcare=hospital` | krankenhaus, hospital, krankenhäuser, hospitals |
| `school` | `amenity=school` | schule, school |
| `museum` | `tourism=museum` | museum, museen, museums |
| `hotel` | `tourism=hotel` | hotel, hotels |
| `restaurant` | `amenity=restaurant` | gasthaus, restaurant, gaststätte, restaurants, gaststätten |
| `cafe` | `amenity=cafe` | café, cafe, kaffee, cafés, cafes, coffee shop |
| `fast_food` | `amenity=fast_food` | fastfood, fast food, imbiss |
| `doctors` | `amenity=doctors` oder `healthcare=doctor` | arzt, ärzte, arztpraxis, doctor |
| `dentist` | `amenity=dentist` oder `healthcare=dentist` | zahnarzt, zahnärzte, zahnarztpraxis, dentist |
| `post_office` | `amenity=post_office` | post, postamt |
| `hairdresser` | `shop=hairdresser` | friseur |
| `playground` | `leisure=playground` | spielplatz |
| `swimming_pool` | `leisure=swimming_pool` oder `leisure=sports_centre` + `sport=swimming` | schwimmbad, schwimmbaeder, schwimmbäder, hallenbad, freibad, swimming pool, swimming pools |
| `pitch` | `leisure=pitch` | sportplatz |
| `park` | `leisure=park` | park, parks |
| `spa` | `leisure=spa` | therme |
| `sauna` | `leisure=sauna` | sauna |
| `forest` | `landuse=forest` oder `natural=wood` | wald, waelder, wälder, wäldchen, forest, forests, wood, woods |
| `river` | `waterway=river` | fluss, river |
| `place_of_worship` | `amenity=place_of_worship` | kirche, church |
| `townhall` | `amenity=townhall` | rathaus |
| `library` | `amenity=library` | bibliothek, library |
| `zoo` | `tourism=zoo` | zoo, tierpark |
| `cinema` | `amenity=cinema` | kino, cinema, kinos, cinemas |
| `fire_station` | `amenity=fire_station` | feuerwehr, feuerwehrhaus, feuerwehrhäuser, fire station |
| `police` | `amenity=police` | polizei, police |
| `lake` | `natural=water` + `water=lake` | see, seen, lake, lakes |
| `charging_station` | `amenity=charging_station` | ladesäule, ladesäulen, ladestation, ladestationen, charging station, ev charging |
| `toilets` | `amenity=toilets` | toilette, toiletten, wc, toilets |
| `bicycle_parking` | `amenity=bicycle_parking` | fahrradparkplatz, fahrradparkplätze, bicycle parking |
| `camp_site` | `tourism=camp_site` | campingplatz, campingplätze, campsite |
| `physiotherapist` | `healthcare=physiotherapist` | physiotherapie, physiotherapist |

`Hallenbad` und `Freibad` gelten als Synonyme für Schwimmbad; nach innen und
außen wird nicht gefiltert. Die Kategorien sagen nichts über öffentliche
Zugänglichkeit oder Öffnungszeiten aus. Wasserflächen ohne `water=lake` gelten
nicht als See.

## Quellen

- Medizinische Alternativen: [OSM-Healthcare-Schema](https://wiki.openstreetmap.org/wiki/Healthcare)
- Seen und andere Gewässer: [natural=water](https://wiki.openstreetmap.org/wiki/Tag:natural%3Dwater), [water](https://wiki.openstreetmap.org/wiki/Key:water)
- Schwimmbäder: [leisure=swimming_pool](https://wiki.openstreetmap.org/wiki/DE:Tag:leisure%3Dswimming_pool)
