# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)
"""Shared client for the VirtoMir API (/api/v1) used by the modules of onurbaydas.virtomir."""

from __future__ import absolute_import, division, print_function

__metaclass__ = type

import json
import time

from ansible.module_utils.basic import env_fallback
from ansible.module_utils.six.moves.urllib.error import HTTPError
from ansible.module_utils.six.moves.urllib.parse import quote
from ansible.module_utils.urls import open_url


def common_args():
    """Options every module takes; module_defaults: group/onurbaydas.virtomir.all sets them once."""
    return dict(
        url=dict(type='str', fallback=(env_fallback, ['VIRTOMIR_URL'])),
        token=dict(type='str', no_log=True, fallback=(env_fallback, ['VIRTOMIR_TOKEN'])),
        validate_certs=dict(type='bool', default=True),
        host=dict(type='str'),
    )


class APIError(Exception):
    def __init__(self, status, message):
        super(APIError, self).__init__('VirtoMir API %s: %s' % (status, message))
        self.status = status
        self.message = message


class Client(object):
    def __init__(self, module):
        p = module.params
        if not p.get('url'):
            module.fail_json(msg='Set url (or VIRTOMIR_URL) to the VirtoMir host, e.g. https://hv1:8420.')
        self.base = p['url'].rstrip('/')
        self.token = p.get('token')
        self.validate = p.get('validate_certs', True)
        self.member = p.get('host')
        self.poll = 2

    def _path(self, path):
        if self.member and path.startswith('/api/v1/'):
            return '/api/v1/hosts/' + quote(self.member, safe='') + path[len('/api/v1'):]
        return path

    def call(self, method, path, body=None):
        headers = {'Accept': 'application/json'}
        data = None
        if body is not None:
            headers['Content-Type'] = 'application/json'
            data = json.dumps(body)
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        try:
            r = open_url(self.base + self._path(path), method=method, data=data, headers=headers,
                         validate_certs=self.validate, timeout=120)
            raw = r.read()
        except HTTPError as e:
            raw = e.read()
            msg = raw.decode('utf-8', 'replace')
            try:
                msg = json.loads(msg).get('error', msg)
            except ValueError:
                pass
            raise APIError(e.code, msg)
        if not raw:
            return None
        return json.loads(raw.decode('utf-8'))

    # ---- machines --------------------------------------------------------------------
    def domains(self):
        return (self.call('GET', '/api/v1/domains') or {}).get('domains', [])

    def domain(self, name):
        for d in self.domains():
            if d.get('name') == name or d.get('uuid', '').lower() == name.lower():
                return d
        return None

    def wait_job(self, job, timeout=3600):
        deadline = time.time() + timeout
        while True:
            j = self.call('GET', '/api/v1/jobs/' + quote(job['id'], safe=''))
            if j.get('state') == 'succeeded':
                return j
            if j.get('state') == 'failed':
                raise APIError(500, 'job %s failed: %s' % (job['id'], j.get('message')))
            if time.time() > deadline:
                raise APIError(504, 'job %s did not finish in time' % job['id'])
            time.sleep(self.poll)

    def action(self, uuid, action):
        self.call('POST', '/api/v1/domains/%s/actions/%s' % (quote(uuid, safe=''), action))

    def wait_running(self, uuid, running, timeout):
        deadline = time.time() + timeout
        while True:
            d = self.domain(uuid)
            if d is None:
                return
            if is_running(d) == running:
                return
            if time.time() > deadline:
                raise APIError(504, '%s is still %s' % (d.get('name'), d.get('state')))
            time.sleep(self.poll)

    def stop(self, uuid, grace=120, force=False):
        d = self.domain(uuid)
        if d is None or not is_running(d):
            return
        if not force:
            try:
                self.action(uuid, 'shutdown')
                self.wait_running(uuid, False, grace)
                return
            except APIError:
                pass
        self.action(uuid, 'force-off')
        self.wait_running(uuid, False, 60)

    def resize(self, uuid, vcpus, memory_mib):
        s = self.call('GET', '/api/v1/domains/%s/settings' % quote(uuid, safe=''))
        s.pop('bootDevices', None)
        s.pop('hotplugMemoryMiB', None)
        if vcpus:
            s['vcpus'] = s['maxVcpus'] = vcpus
            s['sockets'], s['cores'], s['threads'] = 1, vcpus, 1
            pins = s.get('pinning')
            if isinstance(pins, list) and len(pins) != vcpus:
                s['pinning'] = (pins + [''] * vcpus)[:vcpus]
        if memory_mib:
            s['memoryMiB'] = s['currentMemoryMiB'] = memory_mib
        self.call('PUT', '/api/v1/domains/%s/settings' % quote(uuid, safe=''), s)

    # ---- networks --------------------------------------------------------------------
    def networks(self):
        return (self.call('GET', '/api/v1/networks') or {}).get('networks', [])

    def network(self, name):
        for n in self.networks():
            if n.get('name') == name or n.get('uuid', '').lower() == name.lower():
                return n
        return None

    def create_network(self, spec):
        return self.call('POST', '/api/v1/networks', spec)

    def delete_network(self, name):
        return self.call('DELETE', '/api/v1/networks/' + quote(name, safe=''))

    def start_network(self, name):
        return self.call('POST', '/api/v1/networks/%s/actions/start' % quote(name, safe=''))

    def stop_network(self, name):
        return self.call('POST', '/api/v1/networks/%s/actions/stop' % quote(name, safe=''))

    def set_network_autostart(self, name, enabled):
        return self.call('PUT', '/api/v1/networks/%s/autostart' % quote(name, safe=''), {'enabled': bool(enabled)})

    # ---- storage pools ---------------------------------------------------------------
    def pools(self):
        return (self.call('GET', '/api/v1/storage/pools') or {}).get('pools', [])

    def pool(self, name):
        for p in self.pools():
            if p.get('name') == name or p.get('uuid', '').lower() == name.lower():
                return p
        return None

    def create_pool(self, spec):
        return self.call('POST', '/api/v1/storage/pools', spec)

    def delete_pool(self, name):
        return self.call('DELETE', '/api/v1/storage/pools/' + quote(name, safe=''))

    def start_pool(self, name):
        return self.call('POST', '/api/v1/storage/pools/%s/actions/start' % quote(name, safe=''))

    def stop_pool(self, name):
        return self.call('POST', '/api/v1/storage/pools/%s/actions/stop' % quote(name, safe=''))

    def set_pool_autostart(self, name, enabled):
        return self.call('PUT', '/api/v1/storage/pools/%s/autostart' % quote(name, safe=''), {'enabled': bool(enabled)})

    # ---- projects (ADR-0060) ---------------------------------------------------------
    def projects(self):
        return (self.call('GET', '/api/v1/projects') or {}).get('projects', [])

    def project(self, id_or_name):
        for p in self.projects():
            if p.get('id') == id_or_name or p.get('name') == id_or_name:
                return p
        return None

    def create_project(self, spec):
        res = self.call('POST', '/api/v1/projects', spec)
        if isinstance(res, dict) and 'project' in res:
            return res['project']
        return res

    def update_project(self, id, spec):
        res = self.call('PUT', '/api/v1/projects/' + quote(id, safe=''), spec)
        if isinstance(res, dict) and 'project' in res:
            return res['project']
        return res

    def delete_project(self, id):
        return self.call('DELETE', '/api/v1/projects/' + quote(id, safe=''))

    # ---- placement rules (ADR-0062) ---------------------------------------------------
    def placement_rules(self):
        return (self.call('GET', '/api/v1/placement') or {}).get('rules', [])

    def placement_rule(self, id_or_name):
        for r in self.placement_rules():
            if r.get('id') == id_or_name or r.get('name') == id_or_name:
                return r
        return None

    def create_placement_rule(self, spec):
        return self.call('POST', '/api/v1/placement/rules', spec)

    def update_placement_rule(self, id, spec):
        return self.call('PUT', '/api/v1/placement/rules/' + quote(id, safe=''), spec)

    def delete_placement_rule(self, id):
        return self.call('DELETE', '/api/v1/placement/rules/' + quote(id, safe=''))

    # ---- snapshots -------------------------------------------------------------------
    def snapshots(self, uuid):
        return (self.call('GET', '/api/v1/domains/%s/snapshots' % quote(uuid, safe='')) or {}).get('snapshots', [])

    def snapshot(self, uuid, name):
        for s in self.snapshots(uuid):
            if s.get('name') == name:
                return s
        return None

    def create_snapshot(self, uuid, spec):
        return self.call('POST', '/api/v1/domains/%s/snapshots' % quote(uuid, safe=''), spec)

    def revert_snapshot(self, uuid, name):
        return self.call('POST', '/api/v1/domains/%s/snapshots/%s/revert' % (quote(uuid, safe=''), quote(name, safe='')))

    def delete_snapshot(self, uuid, name):
        return self.call('DELETE', '/api/v1/domains/%s/snapshots/%s' % (quote(uuid, safe=''), quote(name, safe='')))


def is_running(d):
    return d.get('state') not in (None, '', 'shutoff', 'crashed', 'pmsuspended')
