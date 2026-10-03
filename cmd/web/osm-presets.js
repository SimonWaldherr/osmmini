/* Presets, typed fields and plain-language labels for the guided OSM editor. */
(function(root){
  'use strict';
  const yesNo=[['yes','Ja'],['no','Nein']];
  const access=[['yes','Ja'],['limited','Teilweise'],['no','Nein']];
  const material=[['wood','Holz'],['metal','Metall'],['stone','Stein'],['concrete','Beton'],['plastic','Kunststoff']];

  const fields=Object.assign(Object.create(null),{
    name:{key:'name',label:'Name',type:'text',placeholder:'Nur falls der Ort einen eigenen Namen hat'},
    opening_hours:{key:'opening_hours',label:'Öffnungszeiten',type:'hours',placeholder:'z. B. Mo-Fr 09:00-18:00',hint:'OSM-Schreibweise mit Bindestrich. Unbekanntes frei lassen.',suggestions:[['24/7','Rund um die Uhr'],['Mo-Fr 09:00-18:00','Mo–Fr 9–18 Uhr'],['Mo-Sa 08:00-20:00','Mo–Sa 8–20 Uhr']]},
    website:{key:'website',label:'Website',type:'url',placeholder:'https://…'},
    phone:{key:'phone',label:'Telefon',type:'tel',placeholder:'+49 …'},
    email:{key:'email',label:'E-Mail',type:'email',placeholder:'name@beispiel.de'},
    operator:{key:'operator',label:'Betreiber',type:'text'},
    ref:{key:'ref',label:'Nummer / Kennung',type:'text',placeholder:'z. B. Nummer vom Schild'},
    wheelchair:{key:'wheelchair',label:'Rollstuhlgerecht',type:'choice',options:access},
    fee:{key:'fee',label:'Kostenpflichtig',type:'choice',options:yesNo},
    covered:{key:'covered',label:'Überdacht',type:'choice',options:yesNo},
    capacity:{key:'capacity',label:'Anzahl Plätze',type:'number'},
    seats:{key:'seats',label:'Anzahl Sitzplätze',type:'number'},
    backrest:{key:'backrest',label:'Rückenlehne',type:'choice',options:yesNo},
    material:{key:'material',label:'Material',type:'choice',options:material},
    bottle:{key:'bottle',label:'Flaschen auffüllbar',type:'choice',options:yesNo},
    seasonal:{key:'seasonal',label:'Nur saisonal',type:'choice',options:yesNo},
    bicycle_parking:{key:'bicycle_parking',label:'Bauart',type:'choice',options:[['stands','Bügel'],['wall_loops','Felgenklemme'],['rack','Ständer'],['shed','Unterstand'],['lockers','Boxen']]},
    waste:{key:'waste',label:'Inhalt',type:'choice',options:[['trash','Restmüll'],['dog_excrement','Hundekot']]},
    changing_table:{key:'changing_table',label:'Wickeltisch',type:'choice',options:yesNo},
    indoor:{key:'indoor',label:'Im Gebäude',type:'choice',options:yesNo},
    access:{key:'access',label:'Zugang',type:'choice',hint:'Beschreibt die Zugangsberechtigung, nicht den Eigentümer. Zeitliche Einschränkungen separat bei den Öffnungszeiten angeben.',options:[['yes','Öffentlich zugänglich'],['permissive','Mit widerruflicher Erlaubnis'],['customers','Nur für Kundschaft'],['private','Nur mit Erlaubnis']]},
    'defibrillator:location':{key:'defibrillator:location',label:'Standort beschreiben',type:'text',placeholder:'z. B. Eingangshalle links'},
    check_date:{key:'check_date',label:'Zuletzt vor Ort geprüft am',type:'text',placeholder:'JJJJ-MM-TT',hint:'Nur nach eigener Prüfung vor Ort eintragen.'},
    source:{key:'source',label:'Datenquelle',type:'text',placeholder:'z. B. survey oder konkrete Quelle',hint:'Bei eigener Vor-Ort-Prüfung wird „survey“ verwendet.'},
    'level:ref':{key:'level:ref',label:'Etage laut Beschilderung',type:'text',placeholder:'z. B. EG oder 1. OG'},
    'fire_hydrant:type':{key:'fire_hydrant:type',label:'Hydrantenart',type:'choice',options:[['underground','Unterflur'],['pillar','Überflur'],['wall','Wandhydrant'],['pipe','Trockener Hydrant (mit Pumpe)']]},
    'fire_hydrant:diameter':{key:'fire_hydrant:diameter',label:'Nennweite (DN)',type:'text',placeholder:'z. B. 80 vom Hydrantenschild',hint:'Wert vom Hydrantenschild übernehmen; nicht den Anschlussdurchmesser am Hydranten.'},
    'fire_hydrant:position':{key:'fire_hydrant:position',label:'Lage',type:'choice',options:[['sidewalk','Gehweg'],['lane','Fahrbahn'],['parking_lot','Parkplatz'],['green','Grünfläche']]},
    'fire_hydrant:pressure':{key:'fire_hydrant:pressure',label:'Wasserdruck',type:'choice',options:[['yes','Drucknetz'],['suction','Sauganschluss']]},
    water_source:{key:'water_source',label:'Wasserquelle',type:'choice',options:[['main','Wassernetz'],['pond','Teich'],['river','Fluss'],['lake','See'],['water_tank','Löschwasserbehälter']]},
    'water_tank:volume':{key:'water_tank:volume',label:'Fassungsvermögen (Liter)',type:'text',placeholder:'z. B. 50000 – nur wenn bekannt'},
    collection_times:{key:'collection_times',label:'Leerungszeiten',type:'text',placeholder:'z. B. Mo-Fr 16:00; Sa 12:00'},
    shelter:{key:'shelter',label:'Wetterschutz',type:'choice',options:yesNo},
    bench:{key:'bench',label:'Sitzgelegenheit',type:'choice',options:yesNo},
    bin:{key:'bin',label:'Mülleimer',type:'choice',options:yesNo},
    cuisine:{key:'cuisine',label:'Küche',type:'text',placeholder:'z. B. italian, regional'},
    outdoor_seating:{key:'outdoor_seating',label:'Außenbereich',type:'choice',options:yesNo},
    takeaway:{key:'takeaway',label:'Zum Mitnehmen',type:'choice',options:yesNo},
    internet_access:{key:'internet_access',label:'WLAN',type:'choice',options:[['wlan','Ja'],['no','Nein']]},
    dispensing:{key:'dispensing',label:'Rezepte werden eingelöst',type:'choice',options:yesNo},
    cash_in:{key:'cash_in',label:'Einzahlungen möglich',type:'choice',options:yesNo},
    vending:{key:'vending',label:'Angebot',type:'choice',options:[['drinks','Getränke'],['food','Essen'],['sweets','Süßes'],['parking_tickets','Parkscheine'],['public_transport_tickets','Fahrkarten']]},
    'recycling:glass_bottles':{key:'recycling:glass_bottles',label:'Glas',type:'choice',options:yesNo},
    'recycling:paper':{key:'recycling:paper',label:'Papier',type:'choice',options:yesNo},
    'recycling:clothes':{key:'recycling:clothes',label:'Kleidung',type:'choice',options:yesNo},
    'recycling:cans':{key:'recycling:cans',label:'Dosen',type:'choice',options:yesNo},
    'service:bicycle:pump':{key:'service:bicycle:pump',label:'Luftpumpe',type:'choice',options:yesNo},
    'service:bicycle:tools':{key:'service:bicycle:tools',label:'Werkzeug',type:'choice',options:yesNo},
    shelter_type:{key:'shelter_type',label:'Art des Unterstands',type:'choice',options:[['public_transport','Haltestelle'],['picnic_shelter','Rastplatz'],['weather_shelter','Wetterschutz']]},
    door:{key:'door',label:'Türtyp',type:'choice',options:[['hinged','Drehtür'],['sliding','Schiebetür'],['revolving','Karussell'],['no','Ohne Tür']]},
    automatic_door:{key:'automatic_door',label:'Automatische Tür',type:'choice',options:yesNo},
    'building:levels':{key:'building:levels',label:'Geschosse über dem Boden',type:'text',placeholder:'z. B. 2',hint:'Ohne Keller und Dachgeschosse; nur bekannte Werte eintragen.'},
    height:{key:'height',label:'Gesamthöhe (Meter)',type:'text',placeholder:'z. B. 8.5'},
    maxspeed:{key:'maxspeed',label:'Höchstgeschwindigkeit',type:'text',placeholder:'z. B. 30 oder walk'},
    oneway:{key:'oneway',label:'Einbahnstraße',type:'choice',options:[['yes','In Zeichenrichtung'],['-1','Gegen Zeichenrichtung'],['no','Nein']]},
    width:{key:'width',label:'Breite (Meter)',type:'text',placeholder:'z. B. 2.5'},
    surface:{key:'surface',label:'Untergrund',type:'text',placeholder:'z. B. sand, gravel'},
    type:{key:'type',label:'Art der Relation',type:'choice',options:[['multipolygon','Multipolygon (Fläche aus mehreren Wegen)'],['route','Route'],['boundary','Grenze'],['restriction','Abbiegebeschränkung'],['associatedStreet','Straße mit Adressen']]},
  });

  // Keys that identify what a place is; shown in plain words in the change summary.
  const keyLabels=Object.assign(Object.create(null),{amenity:'Art des Ortes',leisure:'Art des Ortes',highway:'Art des Ortes',emergency:'Art des Ortes',shop:'Geschäftsart',tourism:'Art des Ortes',building:'Gebäudeart',entrance:'Zugang',description:'Beschreibung',note:'Hinweis',source:'Quelle','contact:phone':'Telefon','contact:website':'Website','contact:email':'E-Mail'});
  const address=['addr:street','addr:housenumber','addr:postcode','addr:city'];
  Object.assign(fields,{
    'addr:street':{key:'addr:street',label:'Straße',type:'text'},
    'addr:housenumber':{key:'addr:housenumber',label:'Hausnummer',type:'text'},
    'addr:postcode':{key:'addr:postcode',label:'Postleitzahl',type:'text'},
    'addr:city':{key:'addr:city',label:'Ort',type:'text'},
  });

  const business=['name','opening_hours','website','phone','wheelchair'];
  const presets=Object.assign(Object.create(null),{
    building:{label:'Gebäude',icon:'🏠',group:'Gebäude & Flächen',geometry:'area',tags:{building:'yes'},fields:['name','building:levels','height']},
    house:{label:'Wohnhaus',icon:'🏠',group:'Gebäude & Flächen',geometry:'area',tags:{building:'house'},fields:['name','building:levels','height']},
    garage:{label:'Garage',icon:'🏠',group:'Gebäude & Flächen',geometry:'area',tags:{building:'garage'},fields:['name','height']},
    grass:{label:'Grünfläche',icon:'🌳',group:'Gebäude & Flächen',geometry:'area',tags:{landuse:'grass'},fields:['name','operator']},
    footway:{label:'Fußweg',icon:'🚶',group:'Straßen & Wege',geometry:'line',tags:{highway:'footway'},fields:['name','surface','width','wheelchair','access']},
    cycleway:{label:'Radweg',icon:'🚲',group:'Straßen & Wege',geometry:'line',tags:{highway:'cycleway'},fields:['name','surface','width','oneway','access']},
    path:{label:'Pfad',icon:'🚶',group:'Straßen & Wege',geometry:'line',tags:{highway:'path'},fields:['name','surface','width','access']},
    residential:{label:'Wohnstraße',icon:'🚗',group:'Straßen & Wege',geometry:'line',tags:{highway:'residential'},fields:['name','maxspeed','oneway','surface','width']},
    service:{label:'Zufahrt',icon:'🚗',group:'Straßen & Wege',geometry:'line',tags:{highway:'service'},fields:['name','maxspeed','oneway','surface','access']},
    cafe:{label:'Café',icon:'☕',group:'Essen & Trinken',tags:{amenity:'cafe'},fields:[...business,'outdoor_seating','internet_access','takeaway']},
    restaurant:{label:'Restaurant',icon:'🍽️',group:'Essen & Trinken',tags:{amenity:'restaurant'},fields:[...business,'cuisine','outdoor_seating','takeaway']},
    fast_food:{label:'Imbiss',icon:'🍔',group:'Essen & Trinken',tags:{amenity:'fast_food'},fields:[...business,'cuisine','outdoor_seating','takeaway']},
    pharmacy:{label:'Apotheke',icon:'💊',group:'Service',tags:{amenity:'pharmacy'},fields:[...business,'dispensing']},
    atm:{label:'Geldautomat',icon:'🏧',group:'Service',tags:{amenity:'atm'},fields:['operator','opening_hours','wheelchair','cash_in']},
    post_box:{label:'Briefkasten',icon:'📮',group:'Service',tags:{amenity:'post_box'},fields:['operator','collection_times','ref']},
    vending_machine:{label:'Automat',icon:'🥤',group:'Service',tags:{amenity:'vending_machine'},fields:['vending','operator','opening_hours']},
    recycling:{label:'Recycling',icon:'♻️',group:'Service',tags:{amenity:'recycling'},fields:['operator','recycling:glass_bottles','recycling:paper','recycling:clothes','recycling:cans']},
    bus_stop:{label:'Bushaltestelle',icon:'🚌',group:'Verkehr',tags:{highway:'bus_stop'},fields:['name','ref','shelter','bench','bin','wheelchair']},
    charging_station:{label:'Ladestation',icon:'🔌',group:'Verkehr',tags:{amenity:'charging_station'},fields:['name','operator','capacity','fee','opening_hours']},
    bicycle_parking:{label:'Fahrradparkplatz',icon:'🚲',group:'Verkehr',tags:{amenity:'bicycle_parking'},fields:['capacity','covered','bicycle_parking','fee']},
    bicycle_repair:{label:'Fahrrad-Reparatur',icon:'🔧',group:'Verkehr',tags:{amenity:'bicycle_repair_station'},fields:['service:bicycle:pump','service:bicycle:tools','opening_hours']},
    bench:{label:'Sitzbank',icon:'🪑',group:'Draußen',tags:{amenity:'bench'},fields:['seats','backrest','material','covered']},
    picnic_table:{label:'Picknicktisch',icon:'🧺',group:'Draußen',tags:{leisure:'picnic_table'},fields:['seats','material','covered']},
    drinking_water:{label:'Trinkwasser',icon:'🚰',group:'Draußen',tags:{amenity:'drinking_water'},fields:['bottle','seasonal','wheelchair']},
    waste_basket:{label:'Mülleimer',icon:'🗑️',group:'Draußen',tags:{amenity:'waste_basket'},fields:['waste','covered']},
    toilets:{label:'Öffentliche Toilette',icon:'🚻',group:'Draußen',tags:{amenity:'toilets'},fields:['fee','wheelchair','changing_table','opening_hours']},
    playground:{label:'Spielplatz',icon:'🎠',group:'Draußen',tags:{leisure:'playground'},fields:['name','surface','wheelchair','opening_hours']},
    shelter:{label:'Unterstand',icon:'⛱️',group:'Draußen',tags:{amenity:'shelter'},fields:['shelter_type','bench','material']},
    fire_hydrant:{label:'Hydrant',icon:'🚒',group:'Feuerwehr & Erste Hilfe',synonyms:['Hydranten','Löschwasserhydrant'],tags:{emergency:'fire_hydrant'},fields:['name','ref','operator','access','fire_hydrant:type','fire_hydrant:diameter','fire_hydrant:position','fire_hydrant:pressure','water_source'],basicFields:['fire_hydrant:type','ref']},
    fire_water_pond:{label:'Löschwasserteich',icon:'🪷',group:'Feuerwehr & Erste Hilfe',synonyms:['Löschweiher','Löschteich','Feuerlöschteich'],tags:{natural:'water',water:'pond',emergency:'fire_water_pond'},fields:['name','ref','operator','access'],basicFields:['ref','access']},
    suction_point:{label:'Löschwasser-Saugstelle',icon:'💧',group:'Feuerwehr & Erste Hilfe',synonyms:['Saugstelle','Löschwasserentnahmestelle'],tags:{emergency:'suction_point'},fields:['name','water_source','ref','operator','access'],basicFields:['water_source','ref','access']},
    water_tank:{label:'Löschwasserbehälter',icon:'🛢️',group:'Feuerwehr & Erste Hilfe',synonyms:['Löschwassertank','Löschwasserzisterne'],tags:{emergency:'water_tank'},fields:['name','water_tank:volume','operator'],basicFields:['water_tank:volume']},
    fire_station:{label:'Feuerwache',icon:'🚒',group:'Feuerwehr & Erste Hilfe',synonyms:['Feuerwehrhaus','Feuerwehrstation'],tags:{amenity:'fire_station'},fields:['name','operator','phone','website','opening_hours'],basicFields:['operator','phone']},
    defibrillator:{label:'AED / Defibrillator',icon:'❤️',group:'Feuerwehr & Erste Hilfe',synonyms:['AED','Automatisierter externer Defibrillator'],tags:{emergency:'defibrillator'},fields:['defibrillator:location','indoor','access','opening_hours','check_date','source','level:ref','phone'],basicFields:['indoor','access','opening_hours','defibrillator:location']},
    barrier_free_entrance:{label:'Barrierefreier Zugang',icon:'♿',group:'Notfall & Zugang',tags:{entrance:'yes',wheelchair:'yes'},fields:['door','automatic_door','wheelchair']},
  });
  const keyLabel=key=>fields[key]?.label||keyLabels[key]||key;
  const genericFields=['name','opening_hours','website','phone','email','wheelchair'];

  // Most specific preset wins, so a wheelchair-accessible entrance beats a plain amenity match.
  const ranked=Object.entries(presets).sort((a,b)=>Object.keys(b[1].tags).length-Object.keys(a[1].tags).length||Number(Object.hasOwn(a[1].tags,'building'))-Number(Object.hasOwn(b[1].tags,'building')));
  const presetKeyForTags=tagSet=>ranked.find(([,preset])=>Object.entries(preset.tags).every(([key,value])=>tagSet[key]===value))?.[0]||'';
  const presetForTags=tagSet=>presets[presetKeyForTags(tagSet)]||null;
  const fieldsFor=tagSet=>{
    const preset=presetForTags(tagSet);
    return {preset,fields:(preset?preset.fields:genericFields).map(key=>fields[key]),address:address.map(key=>fields[key])};
  };

  const categoryNames=Object.assign(Object.create(null),{
    supermarket:'Supermarkt',bakery:'Bäckerei',butcher:'Metzgerei',convenience:'Laden',clothes:'Bekleidung',hairdresser:'Friseur',kiosk:'Kiosk',florist:'Blumenladen',
    bank:'Bank',doctors:'Arztpraxis',dentist:'Zahnarzt',hospital:'Krankenhaus',clinic:'Klinik',school:'Schule',kindergarten:'Kindergarten',library:'Bibliothek',townhall:'Rathaus',
    police:'Polizei',fire_station:'Feuerwehr',place_of_worship:'Kirche / Gebetsstätte',fuel:'Tankstelle',parking:'Parkplatz',bar:'Bar',pub:'Kneipe',biergarten:'Biergarten',ice_cream:'Eisdiele',
    hotel:'Hotel',guest_house:'Pension',museum:'Museum',attraction:'Sehenswürdigkeit',viewpoint:'Aussichtspunkt',park:'Park',pitch:'Sportplatz',sports_centre:'Sportzentrum',
    bus_stop:'Bushaltestelle',platform:'Haltestelle',station:'Bahnhof',halt:'Haltepunkt',tram_stop:'Straßenbahnhalt',residential:'Wohnstraße',service:'Zufahrt',footway:'Fußweg',cycleway:'Radweg',
    house:'Wohnhaus',yes:'Gebäude',commercial:'Gewerbe',industrial:'Industrie',office:'Büro',
    water:'Gewässer',stream:'Bach',river:'Fluss',canal:'Kanal',ditch:'Graben',wood:'Wald',forest:'Wald',grassland:'Wiese',meadow:'Wiese',scrub:'Gebüsch',farmland:'Acker',
    wetland:'Feuchtgebiet',reservoir:'Speicherbecken',garden:'Garten',cemetery:'Friedhof',allotments:'Kleingärten',post_office:'Post',cinema:'Kino',theatre:'Theater',
  });
  const categoryIcons=Object.assign(Object.create(null),{
    water:'💧',stream:'💧',river:'💧',canal:'💧',ditch:'💧',reservoir:'💧',wood:'🌲',forest:'🌲',grassland:'🌿',meadow:'🌿',scrub:'🌿',farmland:'🌾',garden:'🌿',park:'🌳',
    supermarket:'🛒',convenience:'🛒',bakery:'🥖',butcher:'🥩',hospital:'🏥',clinic:'🏥',doctors:'🩺',dentist:'🦷',school:'🏫',kindergarten:'🧸',library:'📚',museum:'🏛️',
    place_of_worship:'⛪',fuel:'⛽',parking:'🅿️',bank:'🏦',post_office:'📮',hotel:'🏨',guest_house:'🏨',fire_station:'🚒',police:'🚓',townhall:'🏛️',bar:'🍺',pub:'🍺',biergarten:'🍺',ice_cream:'🍦',
    station:'🚉',halt:'🚉',tram_stop:'🚋',platform:'🚏',house:'🏠',residential:'🏠',yes:'🏠',cemetery:'🪦',pitch:'⚽',sports_centre:'🏟️',
  });
  const humanize=value=>{const text=String(value||'').replace(/_/g,' ').trim();return text?text[0].toUpperCase()+text.slice(1):'';};
  const categoryLabel=value=>{
    const key=String(value||'').trim();
    if(!key)return '';
    const preset=Object.values(presets).find(p=>Object.values(p.tags).includes(key)&&Object.keys(p.tags).length===1);
    return preset?.label||categoryNames[key]||humanize(key);
  };
  const categoryIcon=value=>{
    const key=String(value||'').trim();
    const preset=Object.values(presets).find(p=>Object.values(p.tags).includes(key)&&Object.keys(p.tags).length===1);
    return preset?.icon||categoryIcons[key]||'📍';
  };
  // The index falls back to the humanised raw value as a title; a German category name reads better.
  const isRawLabel=(label,category)=>!label||String(label).trim().toLowerCase()===String(category||'').replace(/_/g,' ').trim().toLowerCase();
  const primaryCategoryKeys=['shop','amenity','office','tourism','leisure','railway','public_transport','emergency','craft','historic','highway','building'];
  const primaryCategory=tagSet=>{for(const key of primaryCategoryKeys){if(tagSet[key])return tagSet[key];}return '';};

  // Non-blocking checks: OSM data quality hints that never stop a user from saving.
  function checkValue(field,value){
    const text=String(value||'').trim();
    if(!text||!field)return '';
    if(field.key==='check_date'){
      const match=/^(\d{4})-(\d{2})-(\d{2})$/.exec(text);
      if(!match)return 'Datum als JJJJ-MM-TT eingeben.';
      const [,year,month,day]=match,parsed=new Date(Date.UTC(Number(year),Number(month)-1,Number(day)));
      if(parsed.getUTCFullYear()!==Number(year)||parsed.getUTCMonth()!==Number(month)-1||parsed.getUTCDate()!==Number(day))return 'Dieses Datum gibt es nicht.';
      const now=new Date(),today=Date.UTC(now.getFullYear(),now.getMonth(),now.getDate());
      if(parsed.getTime()>today)return 'Das Prüfdatum darf nicht in der Zukunft liegen.';
    }
    if(field.type==='url'){
      try{const url=new URL(text);if(!/^https?:$/.test(url.protocol))return 'Die Adresse sollte mit https:// beginnen.';}catch{return 'Vollständige Adresse mit https:// angeben.';}
    }
    if(field.type==='tel'&&/[^0-9+()\-/ .;]/.test(text))return 'Nur Ziffern, +, Leerzeichen und - verwenden. Mehrere Nummern mit Semikolon trennen.';
    if(field.type==='email'&&!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(text))return 'Das sieht nicht wie eine E-Mail-Adresse aus.';
    if(field.type==='number'&&!/^\d+$/.test(text))return 'Bitte eine ganze Zahl eintragen.';
    if(field.type==='hours'){
      if(/[–—]/.test(text))return 'Normalen Bindestrich (-) statt langem Strich verwenden.';
      if(/[^A-Za-z0-9:;,.\-+/"' ()]/.test(text))return 'Ungewöhnliche Zeichen: OSM-Schreibweise wie Mo-Fr 09:00-18:00 verwenden.';
    }
    return '';
  }

  root.OSMPresets={fields,presets,address,genericFields,keyLabel,presetKeyForTags,presetForTags,fieldsFor,categoryLabel,categoryIcon,primaryCategory,isRawLabel,checkValue};
})(typeof window==='undefined'?globalThis:window);
