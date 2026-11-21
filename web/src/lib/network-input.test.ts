import { describe, expect, it } from 'vitest'
import { macProblem, parseAddressList, parseVLANBridges, vlanBridgesProblem } from './network-input'

describe('macProblem', () => {
  it('accepts unicast addresses in either separator and case', () => {
    expect(macProblem('52:54:00:AB:cd:ef')).toBe('')
    expect(macProblem(' 02-00-00-00-00-01 ')).toBe('')
  })
  it('refuses what a card cannot have', () => {
    expect(macProblem('52:54:00:ab:cd')).not.toBe('')
    expect(macProblem('01:00:5e:00:00:01')).toContain('multicast')
    expect(macProblem('ff:ff:ff:ff:ff:ff')).toContain('multicast')
    expect(macProblem('00:00:00:00:00:00')).not.toBe('')
  })
})

describe('parseAddressList', () => {
  it('splits lines, commas and spaces and drops repeats', () => {
    expect(parseAddressList('2001:db8::25\n2001:db8::26, 2001:db8::25  \n\n')).toEqual(['2001:db8::25', '2001:db8::26'])
    expect(parseAddressList('   ')).toEqual([])
  })
})

describe('VLAN bridges', () => {
  it('reads bridge:uplink lines', () => {
    expect(parseVLANBridges('br1:bond0\n\n br2 : eth3 ')).toEqual([{ bridge: 'br1', uplink: 'bond0' }, { bridge: 'br2', uplink: 'eth3' }])
  })
  it('says what is wrong', () => {
    expect(vlanBridgesProblem(parseVLANBridges('br1:bond0'))).toBe('')
    expect(vlanBridgesProblem(parseVLANBridges('br1'))).toContain('bridge:uplink')
    expect(vlanBridgesProblem(parseVLANBridges('br1:br1'))).toContain('own uplink')
    expect(vlanBridgesProblem(parseVLANBridges('br1:a\nbr1:b'))).toContain('twice')
    expect(vlanBridgesProblem(parseVLANBridges('br 1:bond0'))).not.toBe('')
  })
})
