const {test}=require('node:test');
const assert=require('node:assert/strict');
require('../web/osm-presets.js');
const P=OSMPresets;

test('every preset field resolves to a typed field with a label',()=>{
 for(const [key,preset] of Object.entries(P.presets)){
  assert.ok(preset.label&&preset.icon&&preset.group,key);
  assert.ok(Object.keys(preset.tags).length,key);
  for(const field of preset.fields)assert.ok(P.fields[field]?.label,`${key}: ${field}`);
 }
 for(const key of [...P.genericFields,...P.address])assert.ok(P.fields[key],key);
 for(const field of Object.values(P.fields)){
  assert.ok(['text','url','tel','email','number','hours','choice'].includes(field.type),field.key);
  if(field.type==='choice')assert.ok(field.options.length>=2,field.key);
 }
});

test('identifying tags are unique so a preset is never guessed ambiguously',()=>{
 const seen=new Map();
 for(const [key,preset] of Object.entries(P.presets)){
  const signature=JSON.stringify(Object.entries(preset.tags).sort());
  assert.ok(!seen.has(signature),`${key} duplicates ${seen.get(signature)}`);seen.set(signature,key);
  assert.equal(P.presetKeyForTags(preset.tags),key);
 }
});

test('the most specific preset wins and unknown objects fall back to common fields',()=>{
 assert.equal(P.presetKeyForTags({entrance:'yes',wheelchair:'yes',name:'Tür'}),'barrier_free_entrance');
 assert.equal(P.presetKeyForTags({entrance:'yes'}),'');
 assert.equal(P.presetKeyForTags({amenity:'cafe',name:'Bohne'}),'cafe');
 const {preset,fields,address}=P.fieldsFor({shop:'bakery'});
 assert.equal(preset,null);assert.deepEqual(fields.map(f=>f.key),P.genericFields);assert.equal(address.length,4);
 assert.ok(P.fieldsFor({amenity:'bench'}).fields.some(f=>f.key==='backrest'));
});

test('categories read as German words with a sensible fallback',()=>{
 assert.equal(P.categoryLabel('cafe'),'Café');assert.equal(P.categoryLabel('pharmacy'),'Apotheke');assert.equal(P.categoryLabel('supermarket'),'Supermarkt');
 assert.equal(P.categoryLabel('shoe_repair'),'Shoe repair');assert.equal(P.categoryLabel(''),'');
 assert.equal(P.categoryIcon('bench'),'🪑');assert.equal(P.categoryIcon('unknown'),'📍');
 assert.equal(P.primaryCategory({name:'x',amenity:'cafe',building:'yes'}),'cafe');assert.equal(P.primaryCategory({name:'x'}),'');
});

test('value hints flag common OSM mistakes but stay silent for good input',()=>{
 const f=key=>P.fields[key];
 assert.equal(P.checkValue(f('website'),'https://example.org/a?b=1'),'');assert.match(P.checkValue(f('website'),'example.org'),/https/);assert.match(P.checkValue(f('website'),'ftp://example.org'),/https/);
 assert.equal(P.checkValue(f('phone'),'+49 (8551) 12-34'),'');assert.match(P.checkValue(f('phone'),'call me'),/Ziffern/);
 assert.equal(P.checkValue(f('email'),'a@b.de'),'');assert.match(P.checkValue(f('email'),'a@b'),/E-Mail/);
 assert.equal(P.checkValue(f('capacity'),'12'),'');assert.match(P.checkValue(f('capacity'),'12,5'),/ganze Zahl/);
 assert.equal(P.checkValue(f('opening_hours'),'Mo-Fr 09:00-18:00; Sa 10:00-14:00'),'');assert.equal(P.checkValue(f('opening_hours'),'24/7'),'');
 assert.match(P.checkValue(f('opening_hours'),'Mo–Fr 09:00–18:00'),/Bindestrich/);assert.match(P.checkValue(f('opening_hours'),'täglich ✓'),/Ungewöhnliche/);
 assert.equal(P.checkValue(f('name'),'anything'),'');assert.equal(P.checkValue(f('website'),''),'');
});

test('building and road presets expose geometry fields while POI use stays primary',()=>{
 assert.equal(OSMPresets.presetKeyForTags({building:'yes',amenity:'cafe'}),'cafe');
 assert.equal(OSMPresets.presets.building.geometry,'area');
 assert.ok(OSMPresets.fieldsFor({building:'house'}).fields.some(f=>f.key==='building:levels'));
 assert.equal(OSMPresets.presets.footway.geometry,'line');
 assert.ok(OSMPresets.fieldsFor({highway:'residential'}).fields.some(f=>f.key==='oneway'));
});
