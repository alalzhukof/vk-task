const $ = s => document.querySelector(s);
let editing = null, selected = null, busy = false, me = null;
const statusNames = {open:'Открыта',in_progress:'В работе',done:'Решена'};
const roleNames = {user:'Пользователь',operator:'Оператор',admin:'Администратор'};
const actionNames = {created:'Создана',updated:'Обновлена',deleted:'Удалена',assigned:'Изменён оператор',commented:'Добавлен комментарий'};
async function api(path, method='GET', data) {
  const response = await fetch('/api'+path, {method, headers:{'Content-Type':'application/json'}, body:data===undefined?undefined:JSON.stringify(data)});
  if (!response.ok) {
    const body = await response.json().catch(()=>({}));
    throw new Error(typeof body.detail==='string'?body.detail:'Проверьте данные или повторите запрос');
  }
  return {data:response.status===204?null:await response.json(),cache:response.headers.get('X-Cache')};
}
function notice(message='') { ($('#ticket-dialog').open?$('#detail-notice'):$('#notice')).textContent=message; }
function node(tag, text, cls) {const n=document.createElement(tag);n.textContent=text;if(cls)n.className=cls;if(tag==='button')n.disabled=busy;return n;}
function button(text, fn) {const b=node('button',text);b.type='button';b.onclick=()=>guard(fn);return b;}
function reset() {editing=null;$('#ticket-form').reset();$('#form-title').textContent='Новое обращение';$('#cancel').hidden=true;$('#status-label').hidden=true;}
async function load() {
  me=(await api('/me')).data;
  $('#identity').textContent=me.username+' · '+roleNames[me.role];
  $('#list-title').textContent=me.role==='admin'?'Все заявки':me.role==='operator'?'Мои и назначенные заявки':'Мои заявки';
  $('#admin-panel').hidden=me.role!=='admin';
  const result=await api('/tickets');
  $('#count').textContent=result.data.length;
  $('#cache').textContent=result.cache==='HIT'?'Данные из кеша':'Данные из БД';
  $('#tickets').replaceChildren();
  if(!result.data.length)$('#tickets').append(node('div','Заявок пока нет.','empty'));
  for(const ticket of result.data) {
    const card=node('article','','ticket'), head=node('div','','ticket-head');
    head.append(node('h3',ticket.title),node('span',statusNames[ticket.status],'badge'));
    card.append(head,node('p',ticket.description||'Без описания'),node('p','Автор: '+ticket.owner_username+' · Оператор: '+(ticket.operator_username||'не назначен'),'muted'));
    const actions=node('div','','actions');
    actions.append(button('Обсуждение · '+ticket.comment_count,()=>openDetail(ticket.id)));
    if(me.role==='admin'||ticket.owner_id===me.id){
      actions.append(button('Изменить',async()=>{editing=ticket.id;const f=$('#ticket-form');f.elements.title.value=ticket.title;f.elements.description.value=ticket.description;f.elements.status.value=ticket.status;$('#form-title').textContent='Редактирование заявки';$('#status-label').hidden=false;$('#cancel').hidden=false;f.elements.title.focus();}),button('Удалить',async()=>{if(!confirm('Удалить заявку вместе с комментариями?'))return;await api('/tickets/'+ticket.id,'DELETE');if(editing===ticket.id)reset();await load();}));
    }
    card.append(actions);$('#tickets').append(card);
  }
  const events=(await api('/events')).data;
  $('#events').replaceChildren();
  if(!events.length)$('#events').append(node('p','Журнал пока пуст. Обработка изменений занимает несколько секунд.','muted'));
  for(const event of events){const row=node('div','','event');row.append(node('span',actionNames[event.action]+' · '+event.ticket_id.slice(0,8)),node('time',new Date(event.processed_at).toLocaleString('ru-RU')));$('#events').append(row);}
}
async function loadUsers(){
 const users=(await api('/admin/users?search='+encodeURIComponent($('#user-search').elements.search.value))).data;
 $('#users').replaceChildren();
 for(const user of users){
  const row=node('div','','user-row'), label=node('label','Роль '+user.username),select=document.createElement('select');
  select.setAttribute('aria-label','Роль '+user.username);
  for(const [value,text] of Object.entries(roleNames)){const option=node('option',text);option.value=value;select.append(option);}
  select.value=user.role;label.append(select);
  const save=button('Сохранить роль',async()=>{await api('/admin/users/'+user.id+'/role','PUT',{role:select.value});await loadUsers();notice('Роль сохранена.');});
  row.append(label,save);$('#users').append(row);
 }
 if(!users.length)$('#users').append(node('p','Пользователи не найдены.','muted'));
}
async function openDetail(id){
 const t=(await api('/tickets/'+id)).data;
 const comments=(await api('/tickets/'+id+'/comments')).data;
 selected=id;$('#detail-title').textContent=t.title;$('#detail-description').textContent=t.description;
 $('#detail-meta').textContent='Автор: '+t.owner_username+' · Оператор: '+(t.operator_username||'не назначен');
 $('#detail-notice').textContent='';$('#status-form').elements.status.value=t.status;
 $('#assignment-form').hidden=me.role!=='admin';
 if(me.role==='admin'){
  const operators=(await api('/admin/users?role=operator')).data;
  const select=$('#assignment-form').elements.operator_id;select.replaceChildren();const empty=node('option','Без оператора');empty.value='';select.append(empty);
  for(const op of operators){const opt=node('option',op.username);opt.value=op.id;select.append(opt);}
  // Keep an existing assignment visible even when the operator list reaches its limit.
  if(t.assigned_operator_id&&!operators.some(op=>op.id===t.assigned_operator_id)){const opt=node('option',t.operator_username);opt.value=t.assigned_operator_id;select.append(opt);}
  select.value=t.assigned_operator_id||'';
 }
 $('#comments').replaceChildren();
 if(!comments.length)$('#comments').append(node('p','Комментариев пока нет.','muted'));
 for(const c of comments){const row=node('article','','comment');row.append(node('strong',c.author_username),node('time',new Date(c.created_at).toLocaleString('ru-RU')),node('p',c.body));$('#comments').append(row);}
 if(!$('#ticket-dialog').open)$('#ticket-dialog').showModal();
}
async function guard(fn) { if(busy)return;busy=true;document.querySelectorAll('button').forEach(b=>b.disabled=true);notice();try{await fn();}catch(e){notice(e.message);}finally{busy=false;document.querySelectorAll('button').forEach(b=>b.disabled=false);} }
async function enter(){ $('#auth').hidden=true;$('#workspace').hidden=false;$('#logout').hidden=false;await load();if(me.role==='admin')await loadUsers(); }
$('#auth-form').onsubmit=e=>{e.preventDefault();guard(async()=>{const f=e.target;await api('/login','POST',{username:f.elements.username.value,password:f.elements.password.value});f.elements.password.value='';await enter();});};
$('#register').onclick=()=>guard(async()=>{const f=$('#auth-form');if(!f.reportValidity())return;await api('/register','POST',{username:f.elements.username.value,password:f.elements.password.value});notice('Аккаунт создан. Нажмите «Войти».');});
$('#ticket-form').onsubmit=e=>{e.preventDefault();guard(async()=>{const f=e.target;const data={title:f.elements.title.value,description:f.elements.description.value};if(editing)data.status=f.elements.status.value;await api(editing?'/tickets/'+editing:'/tickets',editing?'PUT':'POST',data);reset();await load();});};
$('#user-search').onsubmit=e=>{e.preventDefault();guard(loadUsers);};
$('#assignment-form').onsubmit=e=>{e.preventDefault();guard(async()=>{await api('/tickets/'+selected+'/assignment','PUT',{operator_id:e.target.elements.operator_id.value||null});await openDetail(selected);await load();});};
$('#status-form').onsubmit=e=>{e.preventDefault();guard(async()=>{await api('/tickets/'+selected+'/status','PATCH',{status:e.target.elements.status.value});await openDetail(selected);await load();});};
$('#comment-form').onsubmit=e=>{e.preventDefault();guard(async()=>{await api('/tickets/'+selected+'/comments','POST',{body:e.target.elements.body.value});e.target.reset();await openDetail(selected);await load();});};
$('#close-detail').onclick=()=>$('#ticket-dialog').close();
$('#cancel').onclick=reset;
$('#refresh').onclick=()=>guard(load);
$('#logout').onclick=()=>guard(async()=>{await api('/logout','POST',{});$('#ticket-dialog').close();$('#workspace').hidden=true;$('#auth').hidden=false;$('#logout').hidden=true;$('#tickets').replaceChildren();$('#events').replaceChildren();$('#users').replaceChildren();me=null;selected=null;reset();});
api('/me').then(enter).catch(()=>{});
