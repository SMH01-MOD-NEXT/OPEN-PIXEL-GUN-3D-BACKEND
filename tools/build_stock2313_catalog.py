#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
"""Join extracted stock registry/labels; never reads player data or preferences."""
import argparse,json,re,pathlib,collections
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--inputs',type=pathlib.Path,required=True)
parser.add_argument('--dump',type=pathlib.Path,required=True)
parser.add_argument('--output',type=pathlib.Path,required=True)
args=parser.parse_args();C=args.inputs
raw=json.load(open(C/'stock2313-item-catalog-raw.json'));lang=json.load(open(C/'stock2313-localization.json'))['English'];weapons={r['index']:r for r in json.load(open(C/'stock2313-item-records.json'))};bytag={r['tag']:r for r in weapons.values()};wear={r['id']:r for r in json.load(open(C/'stock2313-wear-records.json'))}
dump=args.dump.read_text();block=dump.split('// TypeDefIndex: 8931')[1].split('// Namespace:')[0];types={int(n):k for k,n in re.findall(r'public const .*? (\w+) = (\d+);',block)}
editable={10,20,30,40,50,60,65,70,80,1070,1080,1155,1350,1470,1510,1520}
wearskins={r['i']:r for r in json.load(open(C/'stock-embedded-configs/160.json'))['wss']}
skins={r['id']:r for r in json.load(open(C/'stock2313-skin-records.json'))}
gadgets={r['path'].split('/')[-1].lower():r['keys'][0] for r in json.load(open(C/'stock2313-target-component-records.json')) if r['path'].startswith('gadgets/') and r['keys']}

def module_display_name(item_id):
 parts=item_id.removeprefix('mod_').split('_')
 category=parts[-1]
 name_map={'accelerator':'Acceleration','regenerator':'Regeneration','bandoleer':'Bandolier','drummagazine':'Drum Magazine','luckybullet':'Lucky Bullet','astraledge':'Astral Edge','sharpening':'Sharpening','muzzlebrake':'Muzzle Brake','jetfuel':'Jet Fuel'}
 title=lambda word:name_map.get(word,word.title())
 feature=' '.join(title(word) for word in parts[:-1])
 if category=='armor' and parts[0] in {'accelerator','regenerator','camouflage'}:
  return 'Armor '+feature
 return feature+' '+category.title()

for r in raw:
 t,i=r['type'],r['index'];r.update(name=r['id'],name_source='technical_id',category=types.get(t,str(t)),editable=t in editable)
 if t in [10,1070,1310,1340] and r['id'] in bytag:
  w=bytag[r['id']];label=lang.get(w['key'])
  if label:r.update(name=label,name_source='client_23.1.3_en')
  if t==10:r['weapon_category']=w['category']-1
 if r['id'] in wear:
  w=wear[r['id']];label=lang.get(w['key']) or w['readable']
  if label:r.update(name=label,name_source='client_23.1.3_en' if w['key'] in lang else 'client_23.1.3_readable')
 if t==1150 and r['id'].startswith('mod_'):r.update(name=module_display_name(r['id']),name_source='derived_id')
 if t==80 and r['id'].startswith('pet_') and r['id'][4:]:r.update(name=r['id'][4:].replace('_',' ').title(),name_source='derived_codename')
 if t==70 and r['id'].lower() in gadgets and gadgets[r['id'].lower()] in lang:r.update(name=lang[gadgets[r['id'].lower()]],name_source='client_23.1.3_en')
 if t==65 and r['id'] in skins and skins[r['id']]['key'] in lang:r.update(name=lang[skins[r['id']]['key']],name_source='client_23.1.3_en')
 if t==1510 and i in wearskins and wearskins[i]['tt'] in lang:r.update(name=lang[wearskins[i]['tt']],name_source='client_23.1.3_en')
args.output.write_text(json.dumps(raw,ensure_ascii=False,indent=2)+'\n')
print('catalog',len(raw),'named',sum(r['name_source']!='technical_id' for r in raw),'editable',sum(r['editable'] for r in raw));print(collections.Counter(r['name_source'] for r in raw))
