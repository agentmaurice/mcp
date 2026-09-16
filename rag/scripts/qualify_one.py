#!/usr/bin/env python3
"""Exercise RAG through an existing local One using disposable PostgreSQL/Qdrant.

The LLM fixture is deterministic: this verifies transport and retrieval, not
provider quality. No production content or provider credential is used.
"""
import argparse
import hashlib
import http.server
import json
import os
import pathlib
import subprocess
import tempfile
import threading
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--maurice', required=True)
    parser.add_argument('--config', required=True)
    parser.add_argument('--source', required=True, help='RAG source checkout with ../shared available')
    parser.add_argument('--database-dsn', required=True, help='Disposable test database only')
    parser.add_argument('--qdrant-grpc', default='http://127.0.0.1:18634')
    parser.add_argument('--qdrant-http', default='http://127.0.0.1:18633')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    report = {'status': 'running', 'calls': [], 'checks': [], 'llm': 'deterministic local fixture',
              'source_commit': subprocess.check_output(['git', '-C', args.source, 'rev-parse', 'HEAD'], text=True).strip(),
              'source_diff_sha256': hashlib.sha256(subprocess.check_output(['git', '-C', args.source, 'diff'])).hexdigest()}
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report))

    def save():
        output.write_text(json.dumps(report, indent=2, ensure_ascii=False))

    class LLM(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            print('LLM fixture request:', self.path, flush=True)
            if self.path.endswith('/embeddings'):
                result = {'data': [{'object': 'embedding', 'index': 0, 'embedding': [1.0, 0.0, 0.0]}],
                          'model': 'fixture', 'usage': {'prompt_tokens': 1, 'total_tokens': 1}}
            else:
                prompt = json.dumps(body)
                answer = 'Le code de validation est ORCHID-734.' if 'ORCHID-734' in prompt else 'Information absente.'
                result = {'id': 'fixture', 'object': 'chat.completion', 'created': 1,
                          'model': 'fixture', 'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': answer}, 'finish_reason': 'stop'}]}
            data = json.dumps(result).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), LLM)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    collection = 'qualification_' + uuid.uuid4().hex
    agent = None
    owned = {}

    def process_snapshot():
        rows = subprocess.check_output(['ps', '-axo', 'pid=,ppid=,command='], text=True).splitlines()
        processes = {}
        for row in rows:
            fields = row.strip().split(None, 2)
            if len(fields) == 3:
                processes[int(fields[0])] = (int(fields[1]), fields[2])
        selected = {pid for pid, (_, command) in processes.items() if 'go -C ' + str(pathlib.Path(args.source).resolve()) + ' run' in command}
        selected.update(pid for pid, command in owned.items() if processes.get(pid, (None, None))[1] == command)
        for _ in processes:
            children = {pid for pid, (parent, _) in processes.items() if parent in selected}
            if children <= selected:
                break
            selected.update(children)
        return {pid: processes[pid][1] for pid in selected if pid in processes}

    def qdrant(path, method='GET', data=None):
        request = urllib.request.Request(args.qdrant_http + path, method=method,
                                         data=json.dumps(data).encode() if data is not None else None,
                                         headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=15) as response:
            return json.load(response)

    with tempfile.TemporaryDirectory(prefix='rag-one-') as directory:
        root = pathlib.Path(directory)

        def cli(*cmd, check=True):
            p = subprocess.run([args.maurice, '--config', args.config, *map(str, cmd)],
                               cwd=root, capture_output=True, text=True, timeout=120)
            data = json.loads(p.stdout)
            if check:
                assert p.returncode == 0, data
            return p.returncode, data

        try:
            _, context = cli('context', 'current', '--json')
            assert context['api_url'].startswith('http://127.0.0.1:'), 'Use an isolated local One'
            _, agent = cli('test', 'setup', '--fresh', '--save=false', '--deployment-name', collection, '--json')
            aid = agent['agent_id']
            definition = {'name': 'qualify-rag', 'command': 'go',
                          'arguments': ['-C', str(pathlib.Path(args.source).resolve()), 'run', '-mod=readonly', './cmd/rag', '-transport', 'stdio'], 'env': ['GOWORK=off',
                              'DATABASE_DSN=' + args.database_dsn, 'DATABASE_AUTO_MIGRATE=true',
                              'VECTORSTORE_URL=' + args.qdrant_grpc, 'VECTORSTORE_COLLECTION=' + collection,
                              'VECTORSTORE_DESTRUCTIVE_MIGRATION=false', 'LLM_PROVIDER=openai',
                              'LLM_API_KEY=fixture-only', f'LLM_BASE_URL=http://127.0.0.1:{server.server_port}/v1',
                              'LLM_MODEL=fixture', 'LLM_EMBEDDING_MODEL=fixture', 'LLM_EMBEDDING_DIM=3',
                              'LLM_EMBEDDING_RETRY_MAX_ATTEMPTS=1', 'APP_ROLE=all', 'QUEUE_ENABLED=false',
                              'CACHE_ENABLED=false', 'BUFFER_ENABLED=false', 'LOGGING_LEVEL=info']}
            path = root / 'definition.json'
            path.write_text(json.dumps(definition))
            path.chmod(0o600)
            _, installed = cli('test', 'mcp', 'install', '--agent-id', aid, '--file', path,
                               '--wait', '--wait-timeout', '90s', '--json')
            sid = installed['mcp_server_id']
            report['installation'] = installed
            owned.update(process_snapshot())

            def call(name, params, expect_error=False):
                path = root / 'arguments.json'
                path.write_text(json.dumps(params))
                code, data = cli('tools', 'call', sid + '__qualify-rag--' + name,
                                 '--deployment', aid, '--input-file', path, '--timeout', '30', '--json', check=False)
                report['calls'].append({'tool': name, 'input': params, 'response': data})
                save()
                failed = code != 0 or bool(data.get('error')) or data.get('status') == 'runtime_error'
                assert failed == expect_error, data
                result = data.get('result', data)
                if isinstance(result, list):
                    result = json.loads(''.join(item['text'] for item in result if item.get('type') == 'text'))
                return result

            def ingest(title, content, expected):
                job = call('rag_ingest_start', {'deployment_id': aid, 'tenant_id': 'synthetic-a',
                           'title': title, 'content': content, 'duplicate_strategy': 'none'})
                deadline = time.monotonic() + 45
                while time.monotonic() < deadline:
                    status = call('rag_ingest_status', {'job_id': job['job_id']})
                    if status['status'] in ('completed', 'failed'):
                        assert status['status'] == expected, status
                        return status
                    time.sleep(.5)
                raise AssertionError('Ingestion exceeded 45 seconds')

            scope = {'deployment_id': aid, 'tenant_id': 'synthetic-a'}
            ingest('Document synthétique', 'Le code de validation du projet Orchidée est ORCHID-734.', 'completed')
            docs = call('rag_list_documents', scope)
            assert len(docs['documents']) == 1
            document_id = docs['documents'][0]['id']
            query = {'query': 'Quel est le code de validation du projet Orchidée ?', 'max_tokens': 200}
            answer = call('rag_query', dict(scope, **query))
            assert 'ORCHID-734' in answer['answer']
            assert any(c['DocumentID'] == document_id and 'ORCHID-734' in c['Snippet'] for c in answer['citations'])
            other = call('rag_query', dict(query, deployment_id=aid, tenant_id='synthetic-b'))
            assert not other.get('citations') and 'ORCHID-734' not in other.get('answer', '')
            report['checks'].extend(['ingest completed', 'retrieved citation', 'tenant isolation'])
            call('rag_ingest_status', {'job_id': 'invalid'}, expect_error=True)
            call('rag_purge_tenant', dict(scope, confirm=True))
            assert not call('rag_list_documents', scope)['documents']
            for suffix in ('', '_docs'):
                count = qdrant('/collections/' + collection + suffix + '/points/count', 'POST', {'exact': True})
                assert count['result']['count'] == 0, count
            report['checks'].append('purge removes database and both Qdrant collections')

            # A real Qdrant write failure must become a failed job, not completed.
            qdrant('/collections/' + collection, 'DELETE')
            failure = ingest('Échec vectoriel simulé', 'La recette de test exige une écriture vectorielle réussie.', 'failed')
            assert 'vector' in failure['message'].lower()
            assert not call('rag_list_documents', scope)['documents']
            report['checks'].append('Qdrant failure reported without orphan document')
            report['status'] = 'passed'
        except Exception as exc:
            report['status'] = 'failed'
            report['reason'] = str(exc)
        finally:
            owned.update(process_snapshot())
            if agent:
                try:
                    _, removed = cli('test', 'cleanup', '--agent-id', agent['agent_id'],
                                     '--expect-name', agent['agent_name'], '--apply', '--json')
                    report['cleanup'] = removed.get('status') == 'removed_verified'
                except Exception as exc:
                    report['cleanup'] = False
                    report['cleanup_error'] = str(exc)
            for suffix in ('', '_docs'):
                try:
                    qdrant('/collections/' + collection + suffix, 'DELETE')
                except urllib.error.HTTPError as exc:
                    if exc.code != 404:
                        report['cleanup'] = False
            remaining = process_snapshot()
            deadline = time.monotonic() + 10
            while remaining and time.monotonic() < deadline:
                time.sleep(.25)
                remaining = process_snapshot()
            report['residual_processes'] = list(remaining)
            if remaining:
                report['cleanup'] = False
            server.shutdown()
            server.server_close()
            if not report.get('cleanup'):
                report['status'] = 'cleanup_failed'
            save()
    print(json.dumps({k: report.get(k) for k in ('status', 'checks', 'reason', 'cleanup')}, ensure_ascii=False))
    return 0 if report['status'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
