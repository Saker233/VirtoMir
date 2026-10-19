#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_placement_rule
short_description: Manage VM placement constraints in VirtoMir
description:
  - Create, update, or delete placement constraints (VM affinity, VM anti-affinity, VM-to-host affinity) in VirtoMir (ADR-0062).
  - Automatically resolves VM names to UUIDs via the VirtoMir API.
  - Supports check mode and idempotent updates.
version_added: "0.8.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
  name:
    description: Unique rule name.
    type: str
    required: true
  kind:
    description: Placement constraint type.
    type: str
    required: true
    choices: [vm_affinity, vm_anti_affinity, vm_host]
  strength:
    description: Rule enforcement strength (must for hard constraint, should for soft preference).
    type: str
    choices: [must, should]
    default: must
  domains:
    description: List of VM names or UUIDs participating in the rule.
    type: list
    elements: str
    default: []
  hosts:
    description: Target cluster member IDs for kind=vm_host.
    type: list
    elements: str
    default: []
  exclude:
    description: For kind=vm_host, true specifies that the domains must/should NOT run on the given hosts.
    type: bool
    default: false
  enabled:
    description: Whether the rule is actively enforced by placement, DRS, and HA.
    type: bool
    default: true
  state:
    description: Desired rule state.
    type: str
    choices: [present, absent]
    default: present
  url:
    description: VirtoMir address (or VIRTOMIR_URL).
    type: str
  token:
    description: API token (or VIRTOMIR_TOKEN).
    type: str
  validate_certs:
    description: Check the host's certificate.
    type: bool
    default: true
  host:
    description: Cluster member to manage through url.
    type: str
'''

EXAMPLES = r'''
- name: Keep database replicas on separate hosts (anti-affinity)
  onurbaydas.virtomir.virtomir_placement_rule:
    name: db-anti-affinity
    kind: vm_anti_affinity
    strength: must
    domains: [db-primary, db-replica-1, db-replica-2]
    enabled: true
    state: present

- name: Pin latency-critical VM to specific host
  onurbaydas.virtomir.placement_rule:
    name: gateway-pin
    kind: vm_host
    strength: must
    domains: [edge-gw]
    hosts: [hv-dc1-01]
    exclude: false
    enabled: true
    state: present

- name: Remove placement rule
  onurbaydas.virtomir.placement_rule:
    name: obsolete-rule
    state: absent
'''

RETURN = r'''
rule:
  description: The placement rule details as returned by the VirtoMir API.
  returned: when state == present
  type: dict
diff:
  description: Differences between previous and desired state.
  returned: always
  type: dict
'''

from ansible.module_utils.basic import AnsibleModule

try:
    from ansible_collections.onurbaydas.virtomir.plugins.module_utils.virtomir import (
        APIError, Client, common_args
    )
except ImportError:
    from ..module_utils.virtomir import APIError, Client, common_args


def resolve_domains(c, domains):
    resolved = []
    for d in (domains or []):
        d_str = str(d).strip()
        if not d_str:
            continue
        vm = c.domain(d_str)
        if vm and vm.get('uuid'):
            resolved.append(vm['uuid'])
        else:
            resolved.append(d_str)
    return sorted(list(set(resolved)))


def norm_list(items):
    return sorted([str(x).strip() for x in (items or []) if str(x).strip()])


def main():
    args = common_args()
    args.update(
        name=dict(type='str', required=True),
        kind=dict(type='str', required=True, choices=['vm_affinity', 'vm_anti_affinity', 'vm_host']),
        strength=dict(type='str', choices=['must', 'should'], default='must'),
        domains=dict(type='list', elements='str', default=[]),
        hosts=dict(type='list', elements='str', default=[]),
        exclude=dict(type='bool', default=False),
        enabled=dict(type='bool', default=True),
        state=dict(type='str', choices=['present', 'absent'], default='present'),
    )
    module = AnsibleModule(argument_spec=args, supports_check_mode=True)
    p = module.params
    c = Client(module)

    name = p['name'].strip()
    kind = p['kind']
    strength = p['strength']
    exclude = p['exclude']
    enabled = p['enabled']
    state = p['state']

    changed = False
    diff = {'before': {}, 'after': {}}

    try:
        current = c.placement_rule(name)

        if state == 'absent':
            if current is not None:
                changed = True
                diff['before'] = current
                diff['after'] = {'state': 'absent'}
                if not module.check_mode:
                    c.delete_placement_rule(current['id'])
            module.exit_json(changed=changed, diff=diff)

        res_domains = resolve_domains(c, p.get('domains'))
        norm_hosts = norm_list(p.get('hosts'))

        desired_spec = {
            'name': name,
            'kind': kind,
            'strength': strength,
            'domains': res_domains,
            'enabled': enabled,
        }
        if kind == 'vm_host':
            desired_spec['hosts'] = norm_hosts
            desired_spec['exclude'] = exclude

        if current is None:
            changed = True
            diff['before'] = {}
            diff['after'] = desired_spec
            if module.check_mode:
                module.exit_json(changed=True, diff=diff, rule=None)
            created = c.create_placement_rule(desired_spec)
            module.exit_json(changed=True, diff=diff, rule=created)

        # Current rule exists: compare fields
        cur_kind = current.get('kind')
        cur_strength = current.get('strength')
        cur_enabled = current.get('enabled')
        cur_domains = sorted(current.get('domains') or [])
        cur_hosts = norm_list(current.get('hosts'))
        cur_exclude = bool(current.get('exclude', False))

        diffs = {}
        if cur_kind != kind:
            diffs['kind'] = {'before': cur_kind, 'after': kind}
        if cur_strength != strength:
            diffs['strength'] = {'before': cur_strength, 'after': strength}
        if cur_enabled != enabled:
            diffs['enabled'] = {'before': cur_enabled, 'after': enabled}
        if cur_domains != res_domains:
            diffs['domains'] = {'before': cur_domains, 'after': res_domains}
        if kind == 'vm_host':
            if cur_hosts != norm_hosts:
                diffs['hosts'] = {'before': cur_hosts, 'after': norm_hosts}
            if cur_exclude != exclude:
                diffs['exclude'] = {'before': cur_exclude, 'after': exclude}

        if diffs:
            changed = True
            diff['before'] = {k: v['before'] for k, v in diffs.items()}
            diff['after'] = {k: v['after'] for k, v in diffs.items()}
            update_spec = dict(desired_spec)
            update_spec['id'] = current['id']
            if not module.check_mode:
                updated = c.update_placement_rule(current['id'], update_spec)
                current = updated or c.placement_rule(name)

        module.exit_json(changed=changed, diff=diff, rule=current)

    except APIError as e:
        module.fail_json(msg=str(e), status=e.status)


if __name__ == '__main__':
    main()
