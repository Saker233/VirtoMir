#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_network
short_description: Manage virtual networks in VirtoMir
description:
  - Create, update, start, stop, or delete virtual networks (NAT, routed, isolated, bridged) in VirtoMir.
  - Supports check mode and idempotent operation.
version_added: "0.7.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
  name:
    description: Network name.
    type: str
    required: true
  mode:
    description: Network forwarding mode.
    type: str
    choices: [nat, route, isolated, bridge]
    default: nat
  bridge:
    description: Host bridge interface to bind to (required for mode=bridge).
    type: str
  dhcp:
    description: Enable IPv4 DHCP service for the IPv4 subnet. When false, DHCP server is disabled.
    type: bool
    default: true
  ipv4_subnet:
    description: IPv4 CIDR subnet (e.g. 192.168.100.0/24).
    type: str
  ipv4_dhcp_start:
    description: First address of DHCP range.
    type: str
  ipv4_dhcp_end:
    description: Last address of DHCP range.
    type: str
  ipv6_subnet:
    description: Optional IPv6 /64 CIDR (e.g. fd12:3456:789a::/64).
    type: str
  dhcp6:
    description: Enable IPv6 DHCPv6 service for the IPv6 subnet. By default SLAAC (router advertisements) is used without DHCPv6.
    type: bool
    default: false
  ipv6_dhcp_start:
    description: First address of IPv6 DHCP range.
    type: str
  ipv6_dhcp_end:
    description: Last address of IPv6 DHCP range.
    type: str
  autostart:
    description: Automatically start network with host.
    type: bool
    default: true
  state:
    description: Desired network state.
    type: str
    choices: [present, absent, active, inactive]
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
- name: Create NAT network with IPv4 and IPv6
  onurbaydas.virtomir.virtomir_network:
    name: internal-net
    mode: nat
    ipv4_subnet: 192.168.150.0/24
    ipv4_dhcp_start: 192.168.150.100
    ipv4_dhcp_end: 192.168.150.200
    ipv6_subnet: fd12:3456:789a::/64
    autostart: true
    state: present

- name: Ensure bridge network is active
  onurbaydas.virtomir.network:
    name: lan-bridge
    mode: bridge
    bridge: br0
    state: active

- name: Delete a network
  onurbaydas.virtomir.network:
    name: old-net
    state: absent
'''

RETURN = r'''
network:
  description: The network details as returned by the VirtoMir API.
  returned: when state != absent
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


def main():
    args = common_args()
    args.update(
        name=dict(type='str', required=True),
        mode=dict(type='str', choices=['nat', 'route', 'isolated', 'bridge'], default='nat'),
        bridge=dict(type='str'),
        dhcp=dict(type='bool', default=True),
        ipv4_subnet=dict(type='str'),
        ipv4_dhcp_start=dict(type='str'),
        ipv4_dhcp_end=dict(type='str'),
        ipv6_subnet=dict(type='str'),
        dhcp6=dict(type='bool', default=False),
        ipv6_dhcp_start=dict(type='str'),
        ipv6_dhcp_end=dict(type='str'),
        autostart=dict(type='bool', default=True),
        state=dict(type='str', choices=['present', 'absent', 'active', 'inactive'], default='present'),
    )
    module = AnsibleModule(argument_spec=args, supports_check_mode=True)
    p = module.params
    c = Client(module)

    name = p['name']
    state = p['state']
    mode = p['mode']
    autostart = p['autostart']
    bridge = p.get('bridge')
    dhcp = p.get('dhcp', True)
    ipv4_subnet = p.get('ipv4_subnet')
    ipv4_dhcp_start = p.get('ipv4_dhcp_start')
    ipv4_dhcp_end = p.get('ipv4_dhcp_end')
    ipv6_subnet = p.get('ipv6_subnet')
    dhcp6 = p.get('dhcp6', False)
    ipv6_dhcp_start = p.get('ipv6_dhcp_start')
    ipv6_dhcp_end = p.get('ipv6_dhcp_end')

    changed = False
    diff = {'before': {}, 'after': {}}

    try:
        current = c.network(name)

        if state == 'absent':
            if current is not None:
                changed = True
                diff['before'] = {'state': 'present', 'name': name}
                diff['after'] = {'state': 'absent'}
                if not module.check_mode:
                    c.delete_network(name)
            module.exit_json(changed=changed, diff=diff)

        if current is None:
            changed = True
            desired_spec = {
                'name': name,
                'mode': mode,
                'autostart': autostart,
                'state': state,
            }
            if bridge:
                desired_spec['bridge'] = bridge
            if ipv4_subnet:
                desired_spec['ipv4_subnet'] = ipv4_subnet
                desired_spec['dhcp'] = dhcp
            if ipv4_dhcp_start:
                desired_spec['ipv4_dhcp_start'] = ipv4_dhcp_start
            if ipv4_dhcp_end:
                desired_spec['ipv4_dhcp_end'] = ipv4_dhcp_end
            if ipv6_subnet:
                desired_spec['ipv6_subnet'] = ipv6_subnet
                desired_spec['dhcp6'] = dhcp6
            if ipv6_dhcp_start:
                desired_spec['ipv6_dhcp_start'] = ipv6_dhcp_start
            if ipv6_dhcp_end:
                desired_spec['ipv6_dhcp_end'] = ipv6_dhcp_end

            diff['before'] = {}
            diff['after'] = desired_spec

            if module.check_mode:
                module.exit_json(changed=True, diff=diff, network=None)

            api_spec = {
                'name': name,
                'mode': mode,
            }
            if bridge:
                api_spec['bridge'] = bridge
            if ipv4_subnet:
                api_spec['subnet'] = ipv4_subnet
                api_spec['dhcp'] = bool(dhcp)
                if ipv4_dhcp_start:
                    api_spec['dhcpStart'] = ipv4_dhcp_start
                if ipv4_dhcp_end:
                    api_spec['dhcpEnd'] = ipv4_dhcp_end
            if ipv6_subnet:
                api_spec['subnet6'] = ipv6_subnet
                if dhcp6 or ipv6_dhcp_start or ipv6_dhcp_end:
                    api_spec['dhcp6'] = True
                    if ipv6_dhcp_start:
                        api_spec['dhcp6Start'] = ipv6_dhcp_start
                    if ipv6_dhcp_end:
                        api_spec['dhcp6End'] = ipv6_dhcp_end

            c.create_network(api_spec)

            if autostart is False:
                c.set_network_autostart(name, False)

            if state == 'inactive':
                c.stop_network(name)
            elif state == 'active':
                current_net = c.network(name)
                if current_net and not current_net.get('active'):
                    c.start_network(name)

            module.exit_json(changed=True, diff=diff, network=c.network(name))

        # current exists
        cur_autostart = current.get('autostart', False)
        if autostart is not None and cur_autostart != autostart:
            changed = True
            diff.setdefault('before', {})['autostart'] = cur_autostart
            diff.setdefault('after', {})['autostart'] = autostart
            if not module.check_mode:
                c.set_network_autostart(name, autostart)

        is_active = current.get('active', False)
        if state == 'active' and not is_active:
            changed = True
            diff.setdefault('before', {})['active'] = False
            diff.setdefault('after', {})['active'] = True
            if not module.check_mode:
                c.start_network(name)
        elif state == 'inactive' and is_active:
            changed = True
            diff.setdefault('before', {})['active'] = True
            diff.setdefault('after', {})['active'] = False
            if not module.check_mode:
                c.stop_network(name)

        net_res = c.network(name) if not module.check_mode and changed else current
        module.exit_json(changed=changed, diff=diff, network=net_res)

    except APIError as e:
        module.fail_json(msg=str(e), status=e.status)


if __name__ == '__main__':
    main()
