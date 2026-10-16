#!/usr/bin/env python3
"""Generate private combined-service resources for explicit scope IDs."""
import argparse
import hashlib
import json
from prepare import PRIVATE, deployment, kube_secret, secret, service, token, write

parser=argparse.ArgumentParser()
parser.add_argument('--tenant',required=True)
parser.add_argument('--project')
parser.add_argument('--migrate',action='store_true')
args=parser.parse_args()
scopes=[{'TenantID':args.tenant}]
if args.project:
    scopes.append({'TenantID':args.tenant,'ProjectID':args.project})
root=kube_secret('artifact-storage','storage-credentials')
def db(name):
    return {'Driver':'PostgreSQL','DSN':f"postgres://{name}:{token(name+'-db-password')}@artifact-forge-postgres:5432/{name}?sslmode=disable",'AutoMigrate':args.migrate,'MaxIdleConns':2,'MaxOpenConns':5}
config={'AppName':'artifact-forge','AppCode':100,'RunMode':'release','Assembly':'combined','HTTPServer':{'Listen':'127.0.0.1:18080','ReadTimeout':10000,'WriteTimeout':10000,'IdleTimeout':60000},'DB':db('artifact'),'StorageDB':db('storage'),'ForgeTenant':{'Endpoint':'https://forge-tenant-tls.forge-tenant-split.svc.cluster.local','ServiceToken':(PRIVATE/'tenant-service-token').read_text().strip()},'TenantManagement':{'Tokens':[token('bucket-management-token')]},'Storage':{'BackendID':'juicefs-local','Endpoint':'http://juicefs:9000','Region':'us-east-1','AccessKeyID':root['MINIO_ROOT_USER'],'SecretAccessKey':root['MINIO_ROOT_PASSWORD'],'UsePathStyle':True},'StorageHandoff':{'Endpoint':'http://127.0.0.1:18081','Token':token('consumer-token')},'StorageInternal':{'ListenAddress':'127.0.0.1:18081','Consumers':[{'ID':'artifact-forge-orbstack','Tokens':[token('consumer-token')],'Scopes':scopes}]}}
write('config.json',json.dumps(config))
mounts=[{'name':'config','mountPath':'/run/config','readOnly':True},{'name':'tls','mountPath':'/tls','readOnly':True}]
containers=[{'name':'artifact','image':'artifact-go:forge-verification','imagePullPolicy':'Never','command':['/usr/local/bin/artifact-forge-server'],'env':[{'name':'SERVER_CONFIG_PATH','value':'/run/config/config.json'},{'name':'SSL_CERT_FILE','value':'/tls/ca.crt'}],'volumeMounts':mounts,'resources':{'requests':{'cpu':'100m','memory':'128Mi'},'limits':{'memory':'512Mi'}}},{'name':'tls','image':'nginx:1.28.0-alpine','ports':[{'containerPort':8443}],'readinessProbe':{'tcpSocket':{'port':8443}},'volumeMounts':[{'name':'tls','mountPath':'/tls','readOnly':True},{'name':'tls','mountPath':'/etc/nginx/nginx.conf','subPath':'nginx.conf','readOnly':True}]}]
d=deployment('artifact-storage','artifact-forge',containers,[{'name':'config','secret':{'secretName':'artifact-forge-config'}},{'name':'tls','secret':{'secretName':'artifact-forge-tls'}}])
d['spec']['template']['metadata']['annotations']={'checksum/config':hashlib.sha256(json.dumps(config,sort_keys=True).encode()).hexdigest()}
# A TCP probe on TLS alone does not establish application readiness.
d['spec']['template']['spec']['containers'][1]['readinessProbe']={'exec':{'command':['sh','-ec','wget -T 2 -q -O /dev/null http://127.0.0.1:18080/ 2>&1 | grep -q "404"']},'initialDelaySeconds':3,'periodSeconds':5}
resources=[secret('artifact-storage','artifact-forge-config',{'config.json':json.dumps(config)}),d,service('artifact-storage','artifact-forge',443,8443)]
write('service-resources.json',json.dumps({'apiVersion':'v1','kind':'List','items':resources}))
print('Prepared combined service with exact scopes; no credentials printed.')
