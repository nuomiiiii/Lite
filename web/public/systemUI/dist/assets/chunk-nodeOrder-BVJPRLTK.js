const c=(e,t)=>{const r=(e.weight??0)-(t.weight??0);if(r!==0)return r;const u=e.created_at??"",d=t.created_at??"";return u!==d?u<d?-1:1:e.uuid===t.uuid?0:e.uuid<t.uuid?-1:1};export{c};
