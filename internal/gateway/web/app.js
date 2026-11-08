'use strict';

const $ = id => document.getElementById(id);
const imageKeys = ['headphones','keyboard','monitor','backpack','lamp','sneakers','kettle','watch','camera'];
const categoryNames = {all:'Все товары',electronics:'Электроника',home:'Для дома',fashion:'Одежда и аксессуары',sport:'Спорт и движение'};
function readLocal(key,fallback) { try { return JSON.parse(localStorage.getItem(key)) ?? fallback; } catch { return fallback; } }
const state = {
  access:'',refresh:'',user:null,epoch:0,view:'catalog',category:'all',seller:'',sale:false,favoritesOnly:false,
  products:new Map(),catalog:[],stock:new Map(),orders:new Map(),sellerProducts:[],sales:new Map(),search:null,
  favorites:new Set(readLocal('mesto.favorites',[])),cart:new Map(),lastOrder:'',detailSKU:'',loading:false
};
for (const item of readLocal('mesto.cart',[])) {
  if (item && /^[a-zA-Z0-9_-]{1,64}$/.test(item.sku) && Number.isInteger(item.quantity) && item.quantity>0 && item.quantity<=100) state.cart.set(item.sku,item);
}
let publicSocket,privateSocket,publicRetry,privateRetry,refreshing,afterAuth,authMode='login',checkoutBusy=false;
let pendingCheckout,pendingProduct,pendingAdjustment,editingSKU='',restockSKU='',sellerReload;

function node(tag,text,cls) {const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;}
function icon(name) {const s=document.createElementNS('http://www.w3.org/2000/svg','svg');const u=document.createElementNS(s.namespaceURI,'use');u.setAttribute('href','/icons.svg#'+name);s.setAttribute('aria-hidden','true');s.append(u);return s;}
function photo(product,extra='') {const key=imageKeys.includes(product?.image_key)?product.image_key:'headphones';const n=node('div',undefined,'photo photo-'+key+(extra?' '+extra:''));n.setAttribute('role','img');n.setAttribute('aria-label',product?.name||'Товар');return n;}
function button(text,cls,action,iconName) {const b=node('button',undefined,cls);b.type='button';if(iconName)b.append(icon(iconName));if(text)b.append(node('span',text));if(action)b.onclick=action;return b;}
function money(value,currency='RUB') {return new Intl.NumberFormat('ru-RU',{style:'currency',currency:currency||'RUB',minimumFractionDigits:value%100?2:0,maximumFractionDigits:2}).format(value/100);}
function countLabel(n) {const last=n%10,lastTwo=n%100;return n+' '+(last===1&&lastTwo!==11?'товар':last>=2&&last<=4&&(lastTwo<12||lastTwo>14)?'товара':'товаров');}
function shortID(id) {return id.slice(-8).toUpperCase();}
function dateLabel(value) {const date=new Date(value);return Number.isNaN(date.getTime())?'':new Intl.DateTimeFormat('ru-RU',{day:'numeric',month:'long',hour:'2-digit',minute:'2-digit'}).format(date);}
function notify(text) {$('message').textContent=text;$('message').hidden=false;clearTimeout(notify.timer);notify.timer=setTimeout(()=>$('message').hidden=true,5000);}
function showError(id,error) {$(id).textContent=error.message;$(id).hidden=false;}
function openDialog(id) {if(!$(id).open)$(id).showModal();}
function closeDialog(id) {if($(id).open)$(id).close();}
function saveCart() {try{localStorage.setItem('mesto.cart',JSON.stringify([...state.cart.values()]));}catch{};renderCounters();}
function renderCounters() {const amount=[...state.cart.values()].reduce((sum,item)=>sum+item.quantity,0);$('cart-count').textContent=amount;$('cart-count').hidden=!amount;$('favorites-count').textContent=state.favorites.size;$('favorites-count').hidden=!state.favorites.size;}

class ApiError extends Error {constructor(status,detail){const messages={400:'Проверьте заполненные поля.',401:'Неверный email или пароль.',403:'Это действие недоступно для вашего аккаунта.',404:'Товар или заказ не найден.',409:'Данные изменились. Обновите страницу и попробуйте снова.',429:'Слишком много запросов. Подождите немного.',503:'Не удалось связаться с магазином. Попробуйте ещё раз.'};super(detail==='account already exists'?'Аккаунт с таким email уже существует.':messages[status]||'Не получилось выполнить действие. Попробуйте ещё раз.');this.status=status;this.detail=detail;}}
async function rotate() {
  if(!state.refresh)throw new ApiError(401,'');
  if(!refreshing){const epoch=state.epoch;refreshing=(async()=>{const r=await fetch('/api/auth/refresh',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({refresh_token:state.refresh}),signal:AbortSignal.timeout(15000)});if(!r.ok){if(epoch===state.epoch)endSession();throw Error('Войдите в аккаунт ещё раз.');}const t=await r.json();if(epoch!==state.epoch)throw Error('Сессия завершена.');state.access=t.access_token;state.refresh=t.refresh_token;})().finally(()=>refreshing=null);}
  return refreshing;
}
async function api(path,options={},retry=true) {
  const headers={'Content-Type':'application/json',...options.headers};if(state.access)headers.Authorization='Bearer '+state.access;
  let response;try{response=await fetch(path,{...options,headers,signal:options.signal||AbortSignal.timeout(15000)});}catch{throw Error('Нет соединения. Проверьте сеть и попробуйте ещё раз.');}
  if(response.status===401&&state.refresh&&retry&&!path.startsWith('/api/auth/')){await rotate();return api(path,options,false);}
  const result=response.status===204?null:await response.json();if(!response.ok)throw new ApiError(response.status,result?.error);return result;
}
function putProduct(p) {const old=state.products.get(p.sku);if(!old||old.version<=p.version){state.products.set(p.sku,p);if(state.cart.has(p.sku)){state.cart.get(p.sku).product=p;saveCart();}}}
function putStock(stock) {const old=state.stock.get(stock.sku);if(!old||old.version<=stock.version)state.stock.set(stock.sku,stock);}
async function loadProducts(quiet=false) {try{const result=await api('/api/products');for(const p of result.items)putProduct(p);state.catalog=result.items.map(p=>p.sku);renderProducts();if(state.products.has('sku-headphones'))$('hero-price').textContent=money(state.products.get('sku-headphones').price_cents);await loadStock(true);}catch(error){if(!quiet){$('products').replaceChildren();notify(error.message);}}}
async function loadStock(quiet=false) {const skus=[...new Set([...state.catalog,...state.sellerProducts.map(p=>p.sku),...state.cart.keys()])].slice(0,100);if(!skus.length)return;try{const result=await api('/api/stock?skus='+encodeURIComponent(skus.join(',')));for(const stock of result.items)putStock(stock);paintStocks();if($('cart-dialog').open)renderCart();}catch(error){if(!quiet)notify(error.message);}}
function stockLabel(sku) {const stock=state.stock.get(sku);if(!stock)return ['Уточняем наличие','unknown'];if(stock.available===0)return ['Раскупили','out'];if(stock.available<=10)return ['Осталось '+stock.available+' шт.','low'];return ['В наличии · '+stock.available+' шт.',''];}
function stockNode(sku) {const n=node('span',undefined,'availability');n.dataset.stockSku=sku;return n;}
function paintStocks() {
  document.querySelectorAll('[data-stock-sku]').forEach(n=>{const [text,cls]=stockLabel(n.dataset.stockSku);n.textContent=text;n.className='availability'+(cls?' '+cls:'');});
  document.querySelectorAll('[data-add-sku]').forEach(n=>{const stock=state.stock.get(n.dataset.addSku);n.disabled=!stock||stock.available<1;const label=n.querySelector('span');if(label)label.textContent=!stock?'Скоро в продаже':stock.available?'В корзину':'Нет в наличии';});
  document.querySelectorAll('[data-plus-sku]').forEach(n=>{const sku=n.dataset.plusSku;const stock=state.stock.get(sku);n.disabled=!stock||(state.cart.get(sku)?.quantity||0)>=Math.min(100,stock.available);});
  renderSellerStats();
}

function renderProducts() {
  let items=(state.search??state.catalog).map(sku=>state.products.get(sku)).filter(Boolean);
  if(state.category!=='all')items=items.filter(p=>p.category===state.category);
  if(state.seller)items=items.filter(p=>p.seller_id===state.seller);
  if(state.sale)items=items.filter(p=>p.old_price_cents>p.price_cents);
  if(state.favoritesOnly)items=items.filter(p=>state.favorites.has(p.sku));
  if($('stock-only').checked)items=items.filter(p=>(state.stock.get(p.sku)?.available||0)>0);
  const sort=$('sort').value;if(sort==='price-asc')items.sort((a,b)=>a.price_cents-b.price_cents);if(sort==='price-desc')items.sort((a,b)=>b.price_cents-a.price_cents);
  const heading=state.favoritesOnly?'Избранное':state.search!==null?'Результаты поиска':state.seller?(items[0]?.seller_name||'Магазин'):state.sale?'Выгодные находки':state.category!=='all'?categoryNames[state.category]:'Вам понравится';
  $('catalog-title').replaceChildren(document.createTextNode(heading),node('span','.','title-dot'));
  $('catalog-kicker').textContent=state.favoritesOnly?'СОХРАНЕНО ДЛЯ ВАС':state.search!==null?'ТОЧНО ТО, ЧТО ВЫ ИСКАЛИ':'ХОРОШИЙ ВЫБОР КАЖДЫЙ ДЕНЬ';
  $('hero').hidden=state.favoritesOnly||state.search!==null||!!state.seller||state.sale||state.category!=='all';
  $('catalog-description').hidden=state.search===null;$('catalog-description').textContent='По запросу «'+$('query').value.trim()+'»';
  $('products').replaceChildren();for(const p of items)$('products').append(productCard(p));$('catalog-empty').hidden=items.length>0;
  document.querySelectorAll('[data-category]').forEach(n=>n.classList.toggle('active',n.dataset.category===state.category&&!state.sale&&!state.favoritesOnly));$('sale-filter').classList.toggle('active',state.sale);
  renderFilterChips();paintStocks();
}
function renderFilterChips() {const root=$('active-filters');root.replaceChildren();if(state.seller){const p=[...state.products.values()].find(p=>p.seller_id===state.seller);const b=button(p?.seller_name||'Магазин','filter-chip',()=>{state.seller='';renderProducts();},'close');root.append(b);}if(state.favoritesOnly)root.append(button('Избранное','filter-chip',()=>{state.favoritesOnly=false;renderProducts();},'close'));root.hidden=!root.childElementCount;}
function productCard(p) {
  const card=node('article',undefined,'product-card');card.dataset.sku=p.sku;
  const picture=node('div',undefined,'product-picture');const open=button('', 'product-photo-button',()=>openProduct(p.sku));open.setAttribute('aria-label','Подробнее: '+p.name);open.append(photo(p));picture.append(open);
  const favorite=button('','favorite-button'+(state.favorites.has(p.sku)?' selected':''),()=>toggleFavorite(p.sku),'heart');favorite.setAttribute('aria-label',state.favorites.has(p.sku)?'Убрать из избранного':'Добавить в избранное');favorite.setAttribute('aria-pressed',String(state.favorites.has(p.sku)));picture.append(favorite);
  if(p.old_price_cents>p.price_cents)picture.append(node('span','−'+Math.round((1-p.price_cents/p.old_price_cents)*100)+'%','sale-badge'));
  const copy=node('div',undefined,'product-copy');const prices=node('div');prices.append(node('span',money(p.price_cents,p.currency),'product-price'));if(p.old_price_cents>p.price_cents)prices.append(node('span',money(p.old_price_cents,p.currency),'old-price'));
  const title=button(p.name,'product-title',()=>openProduct(p.sku));const seller=button(p.seller_name,'product-seller',()=>filterSeller(p.seller_id),'store');
  copy.append(prices,title,seller,stockNode(p.sku),cartControl(p));card.append(picture,copy);return card;
}
function cartControl(p) {
  const selected=state.cart.get(p.sku);if(selected){const step=node('div',undefined,'stepper');const minus=button('','',()=>changeQuantity(p.sku,-1),'minus');minus.setAttribute('aria-label','Уменьшить количество: '+p.name);const plus=button('','',()=>changeQuantity(p.sku,1),'plus');plus.dataset.plusSku=p.sku;plus.setAttribute('aria-label','Увеличить количество: '+p.name);step.append(minus,node('span',selected.quantity),plus);return step;}
  const add=button('В корзину','product-buy',()=>addToCart(p.sku),'plus');add.dataset.addSku=p.sku;return add;
}
function toggleFavorite(sku) {if(state.favorites.has(sku))state.favorites.delete(sku);else state.favorites.add(sku);try{localStorage.setItem('mesto.favorites',JSON.stringify([...state.favorites]));}catch{}renderCounters();renderProducts();if(state.detailSKU===sku&&$('product-dialog').open)renderDetail();}
function resetFilters(scroll=true) {state.category='all';state.seller='';state.sale=false;state.favoritesOnly=false;state.search=null;$('query').value='';$('stock-only').checked=false;showView('catalog');renderProducts();if(scroll)$('catalog-section').scrollIntoView({behavior:'smooth'});}
function setCategory(category) {state.category=category;state.seller='';state.favoritesOnly=false;state.sale=false;showView('catalog');renderProducts();$('catalog-section').scrollIntoView({behavior:'smooth'});}
function filterSeller(seller) {state.seller=seller;state.category='all';state.search=null;state.favoritesOnly=false;state.sale=false;closeDialog('product-dialog');showView('catalog');renderProducts();$('catalog-section').scrollIntoView({behavior:'smooth'});}
function showView(view) {state.view=view;for(const name of ['catalog','orders','seller'])$(name+'-view').hidden=name!==view;history.replaceState(null,'',view==='catalog'?location.pathname:'#'+view);if(view!=='catalog')window.scrollTo({top:0,behavior:'smooth'});}

function addToCart(sku) {const p=state.products.get(sku),stock=state.stock.get(sku);if(!p||!stock||stock.available<1){notify('Этот товар уже закончился.');return;}const previous=state.cart.get(sku);const qty=(previous?.quantity||0)+1;if(qty>Math.min(100,stock.available)){notify('Больше нет в наличии.');return;}state.cart.set(sku,{sku,quantity:qty,product:p});saveCart();renderProducts();if($('cart-dialog').open)renderCart();if($('product-dialog').open)renderDetail();if(!previous)notify('Добавлено в корзину');}
function changeQuantity(sku,delta) {const item=state.cart.get(sku);if(!item)return;const qty=item.quantity+delta;if(qty<1)state.cart.delete(sku);else{if(delta>0&&qty>Math.min(100,state.stock.get(sku)?.available||0)){notify('Доступное количество изменилось.');return;}item.quantity=qty;}saveCart();renderProducts();if($('cart-dialog').open)renderCart();if($('product-dialog').open)renderDetail();}
function openCart() {renderCart();openDialog('cart-dialog');loadStock(true);}
function renderCart() {
  const root=$('cart');root.replaceChildren();let total=0,amount=0,invalid=false;
  for(const item of state.cart.values()){const p=state.products.get(item.sku)||item.product;if(!p){invalid=true;continue;}total+=p.price_cents*item.quantity;amount+=item.quantity;const stock=state.stock.get(item.sku);if(!stock||stock.available<item.quantity)invalid=true;
    const row=node('article',undefined,'cart-item');const copy=node('div');const title=button(p.name,'product-title',()=>{closeDialog('cart-dialog');openProduct(p.sku);});const seller=node('p',p.seller_name,'muted');const controls=node('div',undefined,'cart-item-controls');controls.append(cartControl(p),node('strong',money(p.price_cents*item.quantity,p.currency),'cart-item-price'));
    const remove=button('Убрать','cart-remove',()=>{state.cart.delete(item.sku);saveCart();renderCart();renderProducts();});copy.append(title,seller,stockNode(item.sku),controls,remove);row.append(photo(p),copy);root.append(row);
  }
  if(!state.cart.size)root.append(emptyState('Здесь будут ваши находки','Добавьте в корзину то, что понравилось.','bag',()=>{closeDialog('cart-dialog');resetFilters();},'Выбрать товары'));
  $('cart-summary').hidden=!state.cart.size;$('cart-items-label').textContent=countLabel(amount);$('cart-subtotal').textContent=money(total);$('cart-total').textContent=money(total);$('cart-warning').hidden=!invalid;$('checkout').disabled=invalid||checkoutBusy;$('checkout').replaceChildren(document.createTextNode(state.user?'Оформить заказ':'Войти и оформить'),icon('arrow'));paintStocks();
}
function openProduct(sku) {state.detailSKU=sku;renderDetail();openDialog('product-dialog');}
function renderDetail() {const p=state.products.get(state.detailSKU);if(!p)return;const layout=node('div',undefined,'detail-layout');const copy=node('div',undefined,'detail-copy');copy.append(node('span',categoryNames[p.category]||'Товар','section-kicker'),node('h2',p.name),node('strong',money(p.price_cents,p.currency),'product-price'));if(p.old_price_cents>p.price_cents)copy.append(node('span',money(p.old_price_cents,p.currency),'old-price'));copy.append(button(p.seller_name,'product-seller',()=>filterSeller(p.seller_id),'store'),stockNode(p.sku),node('p',p.description,'detail-description'),cartControl(p));layout.append(photo(p,'detail-photo'),copy);$('product-detail').replaceChildren(layout);paintStocks();}
function emptyState(title,description,iconName,action,actionLabel) {const root=node('div',undefined,'empty-state');root.append(icon(iconName),node('h3',title),node('p',description));if(action)root.append(button(actionLabel,'button secondary',action));return root;}

function putOrder(o) {const old=state.orders.get(o.id);if(!old||old.version<=o.version){state.orders.set(o.id,o);if(state.view==='orders')renderOrders();if(o.id===state.lastOrder)renderCheckout();if(old?.status==='pending'&&o.status==='confirmed')notify('Заказ №'+shortID(o.id)+' принят');if(old?.status==='pending'&&o.status==='rejected')notify('Один из товаров закончился. Откройте заказ.');}}
const statusInfo={pending:['Оформляем','Проверяем наличие выбранных товаров.','refresh'],confirmed:['Заказ принят','Товары закреплены за вами.','check'],rejected:['Не удалось оформить','Один из товаров закончился. Выберите другой вариант.','close']};
function orderCard(o) {const card=node('article',undefined,'order-card');const heading=node('div',undefined,'order-heading');const id=node('div');id.append(node('div','Заказ №'+shortID(o.id),'order-number'),node('p',dateLabel(o.created_at),'order-date'));heading.append(id,node('strong',money(o.total_cents,o.currency),'order-total'));
  const body=node('div',undefined,'order-body'),status=node('div',undefined,'order-status');const info=statusInfo[o.status]||statusInfo.pending;const badge=node('span',undefined,'status-badge '+o.status);badge.append(icon(info[2]),document.createTextNode(info[0]));status.append(badge,node('p',info[1]));const lines=node('div',undefined,'order-lines');for(const item of o.items){const p=state.products.get(item.sku)||item;const row=node('div',undefined,'order-line');const text=node('div');text.append(node('strong',item.name||p.name||item.sku),node('small',item.quantity+' шт. · '+money(item.price_cents*item.quantity,o.currency)));row.append(photo(p),text);lines.append(row);}body.append(status,lines);card.append(heading,body);return card;}
async function loadOrders(quiet=false) {if(!state.user)return;const epoch=state.epoch;try{const result=await api('/api/orders');if(epoch!==state.epoch)return;for(const o of result.items)putOrder(o);if(state.view==='orders')renderOrders();}catch(error){if(!quiet)notify(error.message);}}
function renderOrders() {const root=$('orders');root.replaceChildren();const orders=[...state.orders.values()].sort((a,b)=>new Date(b.created_at)-new Date(a.created_at)||b.id.localeCompare(a.id));for(const order of orders)root.append(orderCard(order));if(!orders.length)root.append(emptyState('Ваш первый заказ ещё впереди','Присмотритесь к нашей подборке — там много интересного.','box',()=>resetFilters(),'К покупкам'));}
function openOrders() {if(!state.user){openAuth('login','customer',openOrders);return;}showView('orders');renderOrders();loadOrders();}
async function checkout() {if(checkoutBusy||!state.cart.size)return;if(!state.user){openAuth('login','customer',openCart);return;}const items=[...state.cart.values()].map(i=>({sku:i.sku,quantity:i.quantity})).sort((a,b)=>a.sku.localeCompare(b.sku));const body=JSON.stringify({items});if(pendingCheckout?.body!==body||pendingCheckout?.user!==state.user.id)pendingCheckout={body,key:crypto.randomUUID(),user:state.user.id};checkoutBusy=true;renderCart();
  try{const order=await api('/api/orders',{method:'POST',headers:{'Idempotency-Key':pendingCheckout.key},body});putOrder(order);state.lastOrder=order.id;state.cart.clear();pendingCheckout=null;saveCart();renderProducts();closeDialog('cart-dialog');renderCheckout();openDialog('checkout-dialog');loadStock(true);}
  catch(error){notify(error.message);}
  finally{checkoutBusy=false;if($('cart-dialog').open)renderCart();}
}
function renderCheckout() {const o=state.orders.get(state.lastOrder);if(!o)return;const [title,description,mark]=statusInfo[o.status]||statusInfo.pending;$('checkout-title').textContent=o.status==='confirmed'?'Спасибо за заказ!':o.status==='rejected'?'Товар закончился':'Оформляем ваш заказ';$('checkout-description').textContent=o.status==='confirmed'?'Заказ №'+shortID(o.id)+' принят. Он уже в вашем личном кабинете.':description;$('checkout-amount').textContent=money(o.total_cents,o.currency);$('checkout-symbol').className='checkout-symbol '+o.status;$('checkout-symbol').replaceChildren(icon(mark));}

function openAuth(mode='login',role='customer',action) {authMode=mode;afterAuth=action;$('auth-error').hidden=true;$('auth-role').value=role;setAuthMode(mode);openDialog('auth-dialog');}
function setAuthMode(mode) {authMode=mode;const register=mode==='register';$('register-fields').hidden=!register;$('auth-title').textContent=register?($('auth-role').value==='seller'?'Здесь начинается ваш магазин':'Добро пожаловать в mesto'):'Рады вас видеть';$('auth-caption').textContent=register?'Создайте аккаунт — и найдётся ваше.':'Войдите, чтобы оформить заказ и сохранить находки.';$('auth-submit').textContent=register?'Создать аккаунт':'Войти';$('password').autocomplete=register?'new-password':'current-password';document.querySelectorAll('[data-auth-mode]').forEach(n=>n.classList.toggle('active',n.dataset.authMode===mode));const seller=register&&$('auth-role').value==='seller';$('shop-name-label').hidden=!seller;$('shop-name').required=seller;}
async function submitAuth(event) {event.preventDefault();const submit=$('auth-submit');if(submit.disabled)return;submit.disabled=true;$('auth-error').hidden=true;const email=$('email').value.trim(),password=$('password').value;
  try{if(authMode==='register'){const role=$('auth-role').value;await api('/api/auth/register',{method:'POST',body:JSON.stringify({email,password,role,shop_name:role==='seller'?$('shop-name').value.trim():''})});}
    const t=await api('/api/auth/login',{method:'POST',body:JSON.stringify({email,password})});const previousRefresh=state.refresh;if(previousRefresh){try{await api('/api/auth/logout',{method:'POST',body:JSON.stringify({refresh_token:previousRefresh})});}catch{}}
    state.epoch++;state.access=t.access_token;state.refresh=t.refresh_token;state.orders.clear();state.sales.clear();state.sellerProducts=[];state.user=await api('/api/auth/me');$('password').value='';closeDialog('auth-dialog');renderAccount();loadOrders(true);connectPrivate();const action=afterAuth;afterAuth=null;if(action)action();else if(state.user.role==='seller')openSeller();else notify('Вы вошли в аккаунт');
  }catch(error){showError('auth-error',error);}finally{submit.disabled=false;}
}
function renderAccount() {$('account-label').textContent=state.user?'Профиль':'Войти';$('sell-link').replaceChildren(document.createTextNode(state.user?.role==='seller'?'Кабинет продавца':'Продавать на mesto'),node('span','↗'));$('profile-title').textContent=state.user?.shop_name||'Мой аккаунт';$('profile-email').textContent=state.user?.email||'';$('profile-store-label').textContent=state.user?.role==='seller'?'Мой магазин':'Открыть магазин';}
function endSession() {state.epoch++;state.access='';state.refresh='';state.user=null;state.orders.clear();state.sales.clear();state.sellerProducts=[];clearTimeout(privateRetry);if(privateSocket)privateSocket.close();privateSocket=null;closeDialog('profile-dialog');renderAccount();if(state.view!=='catalog')showView('catalog');}
async function logout() {try{await api('/api/auth/logout',{method:'POST',body:JSON.stringify({refresh_token:state.refresh})});}catch{}endSession();notify('Вы вышли из аккаунта');}

function openSeller() {closeDialog('profile-dialog');if(!state.user||!['seller','admin'].includes(state.user.role)){openAuth('register','seller',openSeller);return;}showView('seller');$('seller-title').replaceChildren(document.createTextNode(state.user.shop_name||'Мой магазин'),node('span','.','title-dot'));$('seller-email').textContent=state.user.email;loadSeller();}
async function loadSeller(quiet=false) {if(!state.user||!['seller','admin'].includes(state.user.role))return;const epoch=state.epoch;try{const [products,sales]=await Promise.all([api('/api/seller/products'),api('/api/seller/orders')]);if(epoch!==state.epoch)return;state.sellerProducts=products.items;for(const p of products.items)putProduct(p);for(const o of sales.items)putSale(o);renderSellerProducts();renderSales();await loadStock(true);renderSellerStats();}catch(error){if(!quiet)notify(error.message);}}
function putSale(o) {const old=state.sales.get(o.id);if(!old||old.version<=o.version)state.sales.set(o.id,o);}
function renderSellerStats() {$('stat-products').textContent=state.sellerProducts.length;$('stat-stock').textContent=state.sellerProducts.reduce((n,p)=>n+(state.stock.get(p.sku)?.available||0),0);const confirmed=[...state.sales.values()].filter(o=>o.status==='confirmed'&&o.currency==='RUB');$('stat-orders').textContent=confirmed.length;$('stat-revenue').textContent=money(confirmed.reduce((n,o)=>n+o.total_cents,0));$('seller-sales-count').textContent=state.sales.size;}
function renderSellerProducts() {const root=$('seller-products');root.replaceChildren();for(const p of state.sellerProducts){const row=node('article',undefined,'seller-product-row');const text=node('div');text.append(node('h3',p.name),node('small',p.sku));const actions=node('div',undefined,'seller-row-actions');const edit=button('','icon-button',()=>openProductForm(p.sku),'edit');edit.setAttribute('aria-label','Редактировать '+p.name);actions.append(button('Пополнить','button secondary',()=>openRestock(p.sku),'plus'),edit);row.append(photo(p),text,node('strong',money(p.price_cents),'row-price'),stockNode(p.sku),actions);root.append(row);}if(!state.sellerProducts.length)root.append(emptyState('Полка пока пустая','Добавьте первый товар и задайте доступное количество.','store',()=>openProductForm(),'Добавить товар'));paintStocks();}
function renderSales() {const root=$('seller-sales');root.replaceChildren();const sales=[...state.sales.values()].sort((a,b)=>new Date(b.created_at)-new Date(a.created_at));for(const sale of sales)root.append(orderCard(sale));if(!sales.length)root.append(emptyState('Заказы скоро появятся','Здесь будут покупки товаров вашего магазина.','box'));renderSellerStats();}
function openProductForm(sku='') {editingSKU=sku;pendingProduct=null;$('product-form').reset();$('product-form-error').hidden=true;const p=state.products.get(sku);$('product-form-title').textContent=p?'Редактировать товар':'Новый товар';$('save-product').textContent=p?'Сохранить изменения':'Опубликовать товар';$('initial-stock-label').hidden=!!p;$('product-initial-stock').required=!p;if(p){$('product-name').value=p.name;$('product-price').value=(p.price_cents/100).toFixed(2);$('product-old-price').value=p.old_price_cents?(p.old_price_cents/100).toFixed(2):'';$('product-category').value=p.category;$('product-image').value=p.image_key;$('product-description').value=p.description;}$('product-initial-stock').value=5;openDialog('product-form-dialog');}
async function saveProduct(event) {event.preventDefault();const submit=$('save-product');if(submit.disabled)return;const body={name:$('product-name').value.trim(),price_cents:Math.round(Number($('product-price').value)*100),old_price_cents:Math.round(Number($('product-old-price').value||0)*100),category:$('product-category').value,image_key:$('product-image').value,description:$('product-description').value.trim()};if(!editingSKU)body.initial_stock=Number($('product-initial-stock').value);const raw=JSON.stringify(body);if(pendingProduct?.body!==raw)pendingProduct={body:raw,key:crypto.randomUUID()};submit.disabled=true;$('product-form-error').hidden=true;
  try{const p=await api(editingSKU?'/api/seller/products/'+editingSKU:'/api/seller/products',{method:editingSKU?'PUT':'POST',headers:{'Idempotency-Key':pendingProduct.key},body:raw});putProduct(p);pendingProduct=null;closeDialog('product-form-dialog');await Promise.all([loadSeller(true),loadProducts(true)]);notify(editingSKU?'Изменения сохранены':'Товар опубликован. Готовим его к продаже.');}catch(error){showError('product-form-error',error);}finally{submit.disabled=false;}
}
function openRestock(sku) {restockSKU=sku;pendingAdjustment=null;$('restock-name').textContent=state.products.get(sku)?.name||'Товар';$('restock-quantity').value=5;$('restock-error').hidden=true;openDialog('restock-dialog');}
async function restock(event) {event.preventDefault();const submit=$('restock-submit');if(submit.disabled)return;const delta=Number($('restock-quantity').value);if(!Number.isInteger(delta)||delta<1||delta>100000)return;const body=JSON.stringify({delta});if(pendingAdjustment?.body!==body||pendingAdjustment?.sku!==restockSKU)pendingAdjustment={body,sku:restockSKU,key:crypto.randomUUID()};submit.disabled=true;$('restock-error').hidden=true;
  try{const stock=await api('/api/seller/stock/'+restockSKU+'/adjustments',{method:'POST',headers:{'Idempotency-Key':pendingAdjustment.key},body});putStock(stock);pendingAdjustment=null;closeDialog('restock-dialog');paintStocks();notify('Добавлено на склад: '+delta+' шт.');}catch(error){if(error.status===404)error.message='Товар ещё готовится к продаже. Попробуйте через несколько секунд.';showError('restock-error',error);}finally{submit.disabled=false;}
}

function connectPublic() {clearTimeout(publicRetry);const socket=new WebSocket(location.origin.replace(/^http/,'ws')+'/ws/catalog');publicSocket=socket;socket.onopen=()=>loadStock(true);socket.onmessage=event=>{try{const e=JSON.parse(event.data);if(e.type==='stock.updated'){putStock(e.data);paintStocks();if($('cart-dialog').open)renderCart();if($('stock-only').checked)renderProducts();}else if(e.type==='product.updated'){putProduct(e.data);if(!state.catalog.includes(e.data.sku))state.catalog.unshift(e.data.sku);renderProducts();if(state.user?.id===e.data.seller_id&&state.view==='seller'){clearTimeout(sellerReload);sellerReload=setTimeout(()=>loadSeller(true),350);}}}catch{}};socket.onclose=()=>{if(publicSocket===socket)publicRetry=setTimeout(connectPublic,3000);};socket.onerror=()=>{};}
async function connectPrivate() {if(!state.access)return;clearTimeout(privateRetry);const epoch=state.epoch;try{const {ticket}=await api('/api/ws-ticket',{method:'POST',body:'{}'});if(epoch!==state.epoch)return;if(privateSocket)privateSocket.close();const socket=new WebSocket(location.origin.replace(/^http/,'ws')+'/ws?ticket='+encodeURIComponent(ticket));privateSocket=socket;socket.onopen=()=>loadOrders(true);socket.onmessage=event=>{if(epoch!==state.epoch)return;try{const e=JSON.parse(event.data);if(e.type.startsWith('order.'))putOrder(e.data);if(e.type==='seller.order.updated'){putSale(e.data);if(state.view==='seller')renderSales();renderSellerStats();}}catch{}};socket.onclose=()=>{if(privateSocket===socket&&state.access&&epoch===state.epoch)privateRetry=setTimeout(connectPrivate,2000);};socket.onerror=()=>{};}catch{if(state.access&&epoch===state.epoch)privateRetry=setTimeout(connectPrivate,5000);}}

document.querySelectorAll('[data-close]').forEach(b=>b.onclick=()=>closeDialog(b.dataset.close));
document.querySelectorAll('dialog').forEach(dialog=>dialog.addEventListener('click',event=>{if(event.target===dialog){const rect=dialog.getBoundingClientRect();if(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom)dialog.close();}}));
document.querySelectorAll('.brand,[data-home]').forEach(b=>b.onclick=event=>{event.preventDefault();resetFilters(false);window.scrollTo({top:0,behavior:'smooth'});});
document.querySelectorAll('[data-category]').forEach(b=>b.onclick=()=>setCategory(b.dataset.category));
document.querySelectorAll('[data-auth-mode]').forEach(b=>b.onclick=()=>setAuthMode(b.dataset.authMode));
document.querySelectorAll('[data-tab]').forEach(b=>b.onclick=()=>{document.querySelectorAll('[data-tab]').forEach(n=>n.classList.toggle('active',n===b));$('seller-products-panel').hidden=b.dataset.tab!=='products';$('seller-sales-panel').hidden=b.dataset.tab!=='sales';});
$('account-button').onclick=()=>{if(state.user){renderAccount();openDialog('profile-dialog');}else openAuth();};
$('sell-link').onclick=openSeller;$('seller-banner-button').onclick=openSeller;$('footer-seller').onclick=openSeller;$('profile-store').onclick=openSeller;
$('orders-button').onclick=openOrders;$('profile-orders').onclick=()=>{closeDialog('profile-dialog');openOrders();};$('orders-reload').onclick=()=>loadOrders();
$('cart-button').onclick=openCart;$('checkout').onclick=checkout;$('checkout-orders').onclick=()=>{closeDialog('checkout-dialog');openOrders();};
$('favorites-button').onclick=()=>{resetFilters(false);state.favoritesOnly=true;renderProducts();$('catalog-section').scrollIntoView({behavior:'smooth'});};
$('sale-filter').onclick=()=>{resetFilters(false);state.sale=true;renderProducts();$('catalog-section').scrollIntoView({behavior:'smooth'});};
$('catalog-button').onclick=()=>resetFilters();$('hero-shop').onclick=()=>resetFilters();$('hero-home').onclick=()=>setCategory('home');$('hero-product').onclick=()=>openProduct('sku-headphones');$('reset-filters').onclick=()=>resetFilters();
$('sort').onchange=renderProducts;$('stock-only').onchange=renderProducts;
$('search-form').onsubmit=async event=>{event.preventDefault();const q=$('query').value.trim();if(!q){resetFilters();return;}try{const result=await api('/api/search?q='+encodeURIComponent(q));for(const hit of result.hits.hits)putProduct(hit._source);state.search=result.hits.hits.map(hit=>hit._source.sku);state.category='all';state.seller='';state.favoritesOnly=false;state.sale=false;showView('catalog');renderProducts();$('catalog-section').scrollIntoView({behavior:'smooth'});}catch(error){notify(error.message);}};
$('auth-form').onsubmit=submitAuth;$('auth-role').onchange=()=>setAuthMode(authMode);$('logout').onclick=logout;
$('new-product').onclick=()=>openProductForm();$('product-form').onsubmit=saveProduct;$('restock-form').onsubmit=restock;
setInterval(()=>{if(document.visibilityState!=='visible')return;loadStock(true);if(state.view==='orders'||state.lastOrder&&state.orders.get(state.lastOrder)?.status==='pending')loadOrders(true);if(state.view==='seller')loadSeller(true);},10000);
setInterval(()=>{if(document.visibilityState==='visible'&&state.view==='catalog')loadProducts(true);},45000);
document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible'){loadStock(true);if(state.user)loadOrders(true);}});
renderCounters();renderAccount();loadProducts();connectPublic();
