#!/usr/bin/env python3
"""Prepare the authorized OrbStack deployment; secrets remain in a private directory."""
import base64
import json
import os
from pathlib import Path
import secrets
import subprocess

ROOT = Path(__file__).resolve().parents[2]
PRIVATE = ROOT / '.scratch/forge-orbstack'
PRIVATE.mkdir(mode=0o700, parents=True, exist_ok=True)
PRIVATE.chmod(0o700)
os.umask(0o077)

def run(*args):
    return subprocess.check_output(args, text=True)

def write(name, value):
    p = PRIVATE / name
    p.write_text(value)
    p.chmod(0o600)
    return p

def token(name):
    p = PRIVATE / name
    if not p.exists():
        write(name, secrets.token_hex(32))
    return p.read_text().strip()

def kube_secret(ns, name):
    d = json.loads(run('kubectl', '--context=orbstack', '-n', ns, 'get', 'secret', name, '-o', 'json'))
    return {k: base64.b64decode(v).decode() for k,v in d['data'].items()}

def secret(ns, name, data):
    return {'apiVersion':'v1','kind':'Secret','metadata':{'name':name,'namespace':ns},'type':'Opaque','stringData':data}

def service(ns, name, port, target):
    return {'apiVersion':'v1','kind':'Service','metadata':{'name':name,'namespace':ns},'spec':{'selector':{'app':name},'ports':[{'port':port,'targetPort':target}]}}

def deployment(ns,name,containers,volumes):
    return {'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':name,'namespace':ns},'spec':{'replicas':1,'strategy':{'type':'Recreate'},'selector':{'matchLabels':{'app':name}},'template':{'metadata':{'labels':{'app':name}},'spec':{'containers':containers,'volumes':volumes}}}}

def main():
    assert run('kubectl','config','current-context').strip() == 'orbstack'
    if not (PRIVATE/'ca.key').exists():
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','365','-subj','/CN=Forge OrbStack Integration CA','-keyout',str(PRIVATE/'ca.key'),'-out',str(PRIVATE/'ca.crt')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for name,host in [('artifact','artifact-forge.artifact-storage.svc.cluster.local'),('tenant','forge-tenant-tls.forge-tenant-split.svc.cluster.local')]:
        if not (PRIVATE/f'{name}.crt').exists():
            ext=write(f'{name}.ext',f'subjectAltName=DNS:{host}\nextendedKeyUsage=serverAuth\n')
            subprocess.run(['openssl','req','-newkey','rsa:2048','-nodes','-subj',f'/CN={host}','-keyout',str(PRIVATE/f'{name}.key'),'-out',str(PRIVATE/f'{name}.csr')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            subprocess.run(['openssl','x509','-req','-in',str(PRIVATE/f'{name}.csr'),'-CA',str(PRIVATE/'ca.crt'),'-CAkey',str(PRIVATE/'ca.key'),'-CAcreateserial','-days','365','-extfile',str(ext),'-out',str(PRIVATE/f'{name}.crt')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    admin=token('postgres-admin-password')
    a=token('artifact-db-password')
    s=token('storage-db-password')
    management=token('bucket-management-token')
    consumer=token('consumer-token')
    tenant=kube_secret('forge-tenant-split','forge-tenant-env')
    service_token=tenant.get('SERVICE_TOKEN') or tenant.get('FORGE_SERVICE_TOKEN')
    if not service_token:
        raise SystemExit('tenant service token key not recognized; no credentials printed')
    write('tenant-service-token',service_token)
    resources=[]
    ns='artifact-storage'
    resources.append(secret(ns,'artifact-forge-postgres',{'POSTGRES_PASSWORD':admin,'init.sql':f"CREATE ROLE artifact LOGIN PASSWORD '{a}';\nCREATE ROLE storage LOGIN PASSWORD '{s}';\nCREATE DATABASE artifact OWNER artifact;\nCREATE DATABASE storage OWNER storage;\n"}))
    resources.append({'apiVersion':'v1','kind':'PersistentVolumeClaim','metadata':{'name':'artifact-forge-postgres','namespace':ns},'spec':{'accessModes':['ReadWriteOnce'],'storageClassName':'local-path','resources':{'requests':{'storage':'5Gi'}}}})
    resources.append(deployment(ns,'artifact-forge-postgres',[{'name':'postgres','image':'postgres:17-alpine','imagePullPolicy':'IfNotPresent','env':[{'name':'POSTGRES_PASSWORD','valueFrom':{'secretKeyRef':{'name':'artifact-forge-postgres','key':'POSTGRES_PASSWORD'}}},{'name':'PGDATA','value':'/var/lib/postgresql/data/pgdata'}],'ports':[{'containerPort':5432}],'readinessProbe':{'exec':{'command':['pg_isready','-U','postgres']},'initialDelaySeconds':3},'volumeMounts':[{'name':'data','mountPath':'/var/lib/postgresql/data'},{'name':'init','mountPath':'/docker-entrypoint-initdb.d','readOnly':True}]}],[{'name':'data','persistentVolumeClaim':{'claimName':'artifact-forge-postgres'}},{'name':'init','secret':{'secretName':'artifact-forge-postgres','items':[{'key':'init.sql','path':'init.sql'}]}}]))
    resources.append(service(ns,'artifact-forge-postgres',5432,5432))
    for name,ns,svc,upstream in [('tenant','forge-tenant-split','forge-tenant-tls','http://forge-tenant:80'),('artifact','artifact-storage','artifact-forge','http://127.0.0.1:18080')]:
        conf=f'''events {{}}\nhttp {{ server {{ listen 8443 ssl; ssl_certificate /tls/tls.crt; ssl_certificate_key /tls/tls.key; client_max_body_size 8m; location / {{ proxy_pass {upstream}; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto https; }} }} }}\n'''
        resources.append(secret(ns,svc+'-tls',{'tls.crt':(PRIVATE/f'{name}.crt').read_text(),'tls.key':(PRIVATE/f'{name}.key').read_text(),'ca.crt':(PRIVATE/'ca.crt').read_text(),'nginx.conf':conf}))
        if name=='tenant':
            resources.append(deployment(ns,svc,[{'name':'tls','image':'nginx:1.28.0-alpine','ports':[{'containerPort':8443}],'readinessProbe':{'tcpSocket':{'port':8443}},'volumeMounts':[{'name':'tls','mountPath':'/tls','readOnly':True},{'name':'tls','mountPath':'/etc/nginx/nginx.conf','subPath':'nginx.conf','readOnly':True}]}],[{'name':'tls','secret':{'secretName':svc+'-tls'}}]))
            resources.append(service(ns,svc,443,8443))
    write('base-resources.json',json.dumps({'apiVersion':'v1','kind':'List','items':resources}))
    print('Prepared private base resources and CA; no credentials printed.')

if __name__ == '__main__':
    main()
