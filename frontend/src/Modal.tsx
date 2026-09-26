import {createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject} from 'react';
import {createPortal} from 'react-dom';
import './modal.css';

type Guard = {busy?: boolean; dirty?: boolean};
type Layer = {element: HTMLElement; dismiss: () => void; native: boolean};
const layers: Layer[] = [];
const GuardContext = createContext<Map<object, Guard> | null>(null);

// Only actual editable drafts register dirty state; filters and immediate selections do not.
export function useDialogGuard(guard: Guard) {
 const guards = useContext(GuardContext), key = useRef({});
 useLayoutEffect(() => { guards?.set(key.current, guard); });
 useLayoutEffect(() => () => { guards?.delete(key.current); }, [guards]);
}
function topLayer() {
 return layers.filter(l => l.element.isConnected).sort((a,b) => {
  if(a.native !== b.native) return a.native ? 1 : -1;
  if(a.native) return layers.indexOf(a)-layers.indexOf(b);
  if(a.element.contains(b.element)) return -1;
  if(b.element.contains(a.element)) return 1;
  const z = (e: HTMLElement) => Number(getComputedStyle(e).zIndex)||0;
  return z(a.element)-z(b.element) || (a.element.compareDocumentPosition(b.element)&Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1);
 }).at(-1);
}
function onKey(event: KeyboardEvent) {
 const layer = topLayer(); if(!layer) return;
 if(event.key === 'Escape') {
  event.preventDefault(); event.stopImmediatePropagation();
  if(!event.repeat && !event.isComposing) layer.dismiss();
 }
 if(event.key === 'Tab') {
  const controls = [...layer.element.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),textarea:not(:disabled),select:not(:disabled),a[href],[tabindex="0"]')].filter(e => e.getClientRects().length && !e.closest('[inert]'));
  const first=controls[0], last=controls.at(-1);
  if(!first){event.preventDefault();return;}
  if(!layer.element.contains(document.activeElement) || (event.shiftKey && document.activeElement===first)){event.preventDefault();(event.shiftKey?last:first)?.focus();}
  else if(!event.shiftKey && document.activeElement===last){event.preventDefault();first.focus();}
 }
}
type Props = Guard & {children: ReactNode; close:()=>void; className?:string; open?:boolean; dialogRef?:RefObject<HTMLDialogElement>; 'aria-label'?:string; 'aria-labelledby'?:string;};
function Surface({native,children,close,className,open=true,dialogRef,busy,dirty,...aria}: Props & {native:boolean}) {
 const local = useRef<HTMLDialogElement>(null), div = useRef<HTMLDivElement>(null);
 const ref=dialogRef??local;
 const guards=useRef(new Map<object,Guard>());
 const [confirm,setConfirm]=useState(false),[blocked,setBlocked]=useState(false);
 const latest=useRef({close,busy,dirty}); latest.current={close,busy,dirty};
 const confirmPending=useRef(false), pressed=useRef(false);
 const pendingAction=useRef<(()=>void)|null>(null), replaying=useRef(false);
 const state=()=>({busy:latest.current.busy||[...guards.current.values()].some(g=>g.busy),dirty:latest.current.dirty||[...guards.current.values()].some(g=>g.dirty)});
 const dismiss=(action?:()=>void)=>{
  if(state().busy){setBlocked(true);return;}
  if(state().dirty){if(!confirmPending.current){pendingAction.current=action??null;confirmPending.current=true;setConfirm(true);}return;}
  if(action)action();else latest.current.close();
 };
 const dismissRef=useRef(dismiss); dismissRef.current=dismiss;
 useLayoutEffect(()=>{
  if(!open) return;
  const element=native?ref.current:div.current; if(!element)return;
  const previous=document.activeElement;
  if(native && !(element as HTMLDialogElement).open)(element as HTMLDialogElement).showModal();
  const layer:Layer={element,native,dismiss:()=>dismissRef.current()};
  layers.push(layer); if(layers.length===1)window.addEventListener('keydown',onKey,true);
  // Native dialogs manage initial focus themselves; regular overlays focus after nested registration.
  const frame=requestAnimationFrame(()=>{if(topLayer()===layer&&!element.contains(document.activeElement)) (element.querySelector<HTMLElement>('[autofocus],button:not(:disabled),input:not(:disabled)')??element).focus({preventScroll:true});});
  return()=>{
   cancelAnimationFrame(frame);const wasTop=topLayer()===layer;
   layers.splice(layers.indexOf(layer),1);
   if(!layers.length)window.removeEventListener('keydown',onKey,true);
   if(native)(element as HTMLDialogElement).close();
   if(wasTop && previous instanceof HTMLElement && previous.isConnected)previous.focus({preventScroll:true});
  };
 },[open,native]);
 useEffect(()=>{if(!blocked)return;const timer=setTimeout(()=>setBlocked(false),2800);return()=>clearTimeout(timer);},[blocked]);
 const outside=(event:{target:EventTarget;currentTarget:HTMLElement;clientX:number;clientY:number})=>{
  if(event.target!==event.currentTarget)return false;
  if(!native || className?.includes('app-dialog-backdrop'))return true;
  const r=event.currentTarget.getBoundingClientRect();
  return event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom;
 };
 const events={
  onPointerDown:(e:React.PointerEvent<HTMLElement>)=>{pressed.current=e.button===0&&outside(e);},
  onPointerCancel:()=>{pressed.current=false;},
  onClick:(e:React.MouseEvent<HTMLElement>)=>{const hit=pressed.current&&outside(e);pressed.current=false;if(hit&&topLayer()?.element===e.currentTarget){e.stopPropagation();dismiss();}},
  onClickCapture:(e:React.MouseEvent<HTMLElement>)=>{
   if(replaying.current || topLayer()?.element!==e.currentTarget)return;
   const target=(e.target as HTMLElement).closest<HTMLElement>('[data-dialog-dismiss],[data-dialog-transition]');
   if(!target)return;
   if(target.hasAttribute('data-dialog-transition')){
    if(!state().busy&&!state().dirty)return;
    e.preventDefault();e.stopPropagation();
    dismiss(()=>{replaying.current=true;try{target.click();}finally{replaying.current=false;}});
   } else {e.preventDefault();e.stopPropagation();dismiss();}
  },
 };
 const cancelConfirm=()=>{confirmPending.current=false;pendingAction.current=null;setConfirm(false);};
 const discard=()=>{const action=pendingAction.current;cancelConfirm();if(state().busy)setBlocked(true);else if(action)action();else latest.current.close();};
 const content=<GuardContext.Provider value={guards.current}>{children}{blocked&&<div className="modal-busy-notice" role="status">操作正在进行，请等待完成或先取消操作。</div>}{confirm&&createPortal(<ModalDialog className="modal-discard-dialog" close={cancelConfirm} aria-label="放弃未保存的修改"><h3>放弃未保存的修改？</h3><p>离开后，本次尚未保存的修改将丢失。</p><footer><button type="button" autoFocus onClick={cancelConfirm}>继续编辑</button><button type="button" onClick={discard}>放弃修改</button></footer></ModalDialog>,document.body)}</GuardContext.Provider>;
 if(native)return <dialog {...aria} {...events} ref={ref} className={className} onCancel={e=>{e.preventDefault();if(topLayer()?.element===e.currentTarget)dismiss();}}>{content}</dialog>;
 return <div {...events} ref={div} className={className}>{content}</div>;
}
export function ModalBackdrop(props:Props){return <Surface {...props} native={false}/>;}
export function ModalDialog(props:Props){return <Surface {...props} native/>;}
