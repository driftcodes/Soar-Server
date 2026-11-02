const path=require('node:path');

// Per-account tunnel domains: create and edit restrictions in the account
// dialog, verify the API and subscription, then restore a single-domain server.
module.exports=async({page,out,assert})=>{
 const form=page.locator('#client-form'),name='Domain-limited browser account';
 const read=()=>page.evaluate(async name=>(await api('/clients')).clients.find(c=>c.name===name),name);
 const subscriptionDomains=async id=>page.evaluate(async id=>{const sub=await api('/clients/'+id+'/subscription');const text=await (await fetch(sub.url+'?format=plain')).text();return text.trim().split('\n').map(line=>JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(line.split('://')[1].replace(/-/g,'+').replace(/_/g,'/')),c=>c.charCodeAt(0)))).profile.server.domain);},id);
 const original=await page.evaluate(()=>api('/tunnel-domains'));
 try{
  await page.evaluate(()=>location.hash='clients');await page.locator('#page-clients').waitFor({state:'visible'});
  // One domain: nothing to choose, so the field stays out of the way.
  await page.locator('#page-clients .new-client').click();await page.locator('#client-dialog').waitFor({state:'visible'});
  await page.waitForTimeout(300);assert.equal(await page.locator('#client-zones').isHidden(),true,'single-domain servers do not show a domain picker');
  await page.locator('#client-dialog .close-dialog').first().click();
  await page.evaluate(zone=>api('/tunnel-domains','PUT',{zones:[zone,'t.example.net'],primary:zone}),original.primary);
  await page.locator('#page-clients .new-client').click();
  await page.locator('#client-zones').waitFor({state:'visible'});
  assert.equal(await form.locator('[name=all_zones]').isChecked(),true,'new accounts default to every domain');
  assert.equal(await page.locator('#client-zone-options').isHidden(),true);
  await form.locator('[name=name]').fill(name);
  await form.locator('[name=all_zones]').uncheck();
  const options=page.locator('#client-zone-options input');await options.first().waitFor({state:'visible'});
  assert.deepEqual(await options.evaluateAll(list=>list.map(i=>[i.value,i.checked])),[[original.primary,true],['t.example.net',true]],'unchecking “all” starts from every current domain');
  for(const input of await options.all())await input.uncheck();
  await form.locator('[type=submit]').click();
  assert.match(await page.locator('#client-form-error').innerText(),/دامنه/,'at least one domain is required');
  await page.locator('#client-zone-options input[value="t.example.net"]').check();
  await page.screenshot({path:path.join(out,'account-domains-1440.png')});
  await form.locator('[type=submit]').click();await page.locator('#client-dialog').waitFor({state:'hidden'});
  let account=await read();
  assert.deepEqual(account.tunnel_zones,['t.example.net']);assert.match(account.connect_zone,/\.t\.example\.net$/);
  assert.deepEqual((await subscriptionDomains(account.id)).map(d=>d.endsWith('.t.example.net')),[true,true],'subscription lists only the allowed domain');
  // Editing keeps and then changes the restriction.
  const row=page.locator('#clients-body tr').filter({hasText:name});
  await row.getByRole('button',{name:'ویرایش',exact:true}).click();await page.locator('#client-zones').waitFor({state:'visible'});
  assert.equal(await form.locator('[name=all_zones]').isChecked(),false);
  assert.deepEqual(await page.locator('#client-zone-options input').evaluateAll(list=>list.map(i=>[i.value,i.checked])),[[original.primary,false],['t.example.net',true]]);
  await page.locator(`#client-zone-options input[value="${original.primary}"]`).check();await page.locator('#client-zone-options input[value="t.example.net"]').uncheck();
  await form.locator('[type=submit]').click();await page.locator('#client-dialog').waitFor({state:'hidden'});
  account=await read();assert.deepEqual(account.tunnel_zones,[original.primary]);
  assert.ok((await subscriptionDomains(account.id)).every(d=>d.endsWith('.'+original.primary)));
  // The enable/disable toggle does not drop the restriction.
  const toggle=()=>page.evaluate(async id=>{const c=(await api('/clients')).clients.find(c=>c.id===id);await api('/clients/'+id,'PUT',{engines:c.engines,name:c.name,note:c.note,enabled:!c.enabled,quota_bytes:c.quota_bytes,rate_bytes:c.rate_bytes,stream_limit:c.stream_limit,expires_at:c.expires_at,revision:c.revision});await refresh();},account.id);
  await toggle();account=await read();assert.equal(account.enabled,false);assert.deepEqual(account.tunnel_zones,[original.primary]);
  await toggle();account=await read();assert.equal(account.enabled,true);assert.deepEqual(account.tunnel_zones,[original.primary]);
  // Removing that only domain is refused with a named account.
  const refusal=await page.evaluate(()=>api('/tunnel-domains','PUT',{zones:['t.example.net'],primary:'t.example.net'}).then(()=>'',e=>e.message));
  assert.match(refusal,/Domain-limited browser account/);
  // Back to all domains, in English, on a phone.
  await row.getByRole('button',{name:'ویرایش',exact:true}).click();await page.locator('#client-zones').waitFor({state:'visible'});
  await form.locator('[name=all_zones]').check();assert.equal(await page.locator('#client-zone-options').isHidden(),true);
  await form.locator('[type=submit]').click();await page.locator('#client-dialog').waitFor({state:'hidden'}).catch(async e=>{throw Error('save failed: '+await page.locator('#client-form-error').innerText());});
  account=await read();assert.equal(account.tunnel_zones,undefined);assert.equal((await subscriptionDomains(account.id)).length,4);
  await page.setViewportSize({width:375,height:900});
  await row.getByRole('button',{name:'ویرایش',exact:true}).click();await page.locator('#client-zones').waitFor({state:'visible'});
  await form.locator('[name=all_zones]').uncheck();
  assert.ok(await page.evaluate(()=>document.querySelector('#client-dialog').scrollWidth<=document.querySelector('#client-dialog').clientWidth+1),'domain picker overflows on a phone');
  await page.screenshot({path:path.join(out,'account-domains-375.png')});
  await page.locator('#client-dialog .close-dialog').first().click();
  await page.setViewportSize({width:1440,height:1100});
  // The dialog refreshes the domain list in the background. Hold that request to
  // land the response mid-edit; it must never change "limited" into "all".
  const holdDomainList=async()=>{let release;const held=new Promise(r=>release=r);await page.route('**/api/tunnel-domains',async route=>{if(route.request().method()!=='GET')return route.fallback();await held;await route.continue();},{times:1});return async()=>{const done=page.waitForResponse(r=>r.url().endsWith('/api/tunnel-domains')&&r.request().method()==='GET');release();await done;await page.evaluate(()=>new Promise(r=>setTimeout(r,50)));};};
  let releaseDomains=await holdDomainList();
  await page.locator('#page-clients .new-client').click();await page.locator('#client-zones').waitFor({state:'visible'});
  await form.locator('[name=name]').fill('Delayed domain list');await form.locator('[name=all_zones]').uncheck();
  for(const input of await page.locator('#client-zone-options input').all())await input.uncheck();
  await releaseDomains();
  assert.equal(await form.locator('[name=all_zones]').isChecked(),false,'a late domain list switched a limited selection to all domains');
  assert.equal(await page.locator('#client-zone-options').isVisible(),true);
  assert.equal(await page.locator('#client-zone-options input:checked').count(),0,'a late domain list changed the selection');
  await form.locator('[type=submit]').click();
  assert.match(await page.locator('#client-form-error').innerText(),/دامنه/,'an empty limited selection must not save');
  assert.equal(await page.evaluate(async()=>(await api('/clients')).clients.find(c=>c.name==='Delayed domain list')),undefined);
  await page.locator('#client-dialog .close-dialog').first().click();
  // Single-domain server: switching a limited account to "all" must stick even if
  // the domain list arrives after the click.
  await page.evaluate(async({id,zone})=>{const c=(await api('/clients')).clients.find(c=>c.id===id);await api('/clients/'+id,'PUT',{engines:c.engines,name:c.name,note:c.note,enabled:c.enabled,quota_bytes:c.quota_bytes,rate_bytes:c.rate_bytes,stream_limit:c.stream_limit,expires_at:c.expires_at,revision:c.revision,tunnel_zones:[zone]});await api('/tunnel-domains','PUT',{zones:[zone],primary:zone});await refresh();},{id:account.id,zone:original.primary});
  account=await read();assert.deepEqual(account.tunnel_zones,[original.primary]);
  releaseDomains=await holdDomainList();
  await row.getByRole('button',{name:'ویرایش',exact:true}).click();await page.locator('#client-zones').waitFor({state:'visible'});
  await form.locator('[name=all_zones]').check();
  await releaseDomains();
  assert.equal(await page.locator('#client-zones').isVisible(),true,'the domain field disappeared while being edited');
  assert.equal(await form.locator('[name=all_zones]').isChecked(),true);
  await form.locator('[type=submit]').click();await page.locator('#client-dialog').waitFor({state:'hidden'});
  account=await read();assert.equal(account.tunnel_zones,undefined,'switching to all domains was not saved on a single-domain server');
  await page.evaluate(id=>api('/clients/'+id,'DELETE'),account.id);
 }finally{
  await page.evaluate(d=>api('/tunnel-domains','PUT',{zones:d.zones,primary:d.primary}),original).catch(()=>{});
 }
 console.log('PASS: per-account tunnel domains in create/edit, required selection, subscription filtering, toggle preservation, removal guard, late domain-list responses and phone layout.');
};
