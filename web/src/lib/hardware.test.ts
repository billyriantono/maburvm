import { describe, expect, it } from 'vitest'
import { cpuModelWord, hardwareChanged, hardwareProblem, hardwareSummary } from './hardware'

describe('hardwareSummary', () => {
  it('says nothing for a plain VM', () => {
    expect(hardwareSummary({})).toEqual([])
    expect(hardwareSummary(undefined)).toEqual([])
  })
  it('words each part', () => {
    expect(hardwareSummary({ cpu_model: 'Haswell', sockets: 2, threads: 2, dedicated_cpu: true, pinned_cpus: '4-11', numa: true, firmware: 'uefi', secure_boot: true, tpm: true, restart_on_crash: true }, 8)).toEqual([
      'CPU: Haswell',
      '2 sockets × 2 cores × 2 threads',
      'Dedicated cores 4-11',
      'Memory on the NUMA nodes of its cores',
      'UEFI with Secure Boot',
      'Virtual TPM 2.0',
      'Started again when it crashes',
    ])
  })
  it('gives the shape without cores when they do not divide', () => {
    expect(hardwareSummary({ sockets: 2 }, 3)).toEqual(['2 sockets, 1 thread per core'])
  })
})

describe('hardwareProblem', () => {
  it('catches what the panel refuses', () => {
    expect(hardwareProblem({ numa: true })).toMatch(/NUMA/)
    expect(hardwareProblem({ cpu_model: 'bad model' })).toMatch(/CPU model/)
    expect(hardwareProblem({ sockets: 9 })).toMatch(/Sockets/)
    expect(hardwareProblem({ dedicated_cpu: true, numa: true, cpu_model: 'host-model' })).toBeNull()
  })
})

describe('hardwareChanged', () => {
  it('ignores unset and false alike', () => {
    expect(hardwareChanged({ watchdog: false }, {})).toBe(false)
    expect(hardwareChanged({}, { hugepages: true })).toBe(true)
    expect(hardwareChanged(undefined, { cpu_model: '' })).toBe(false)
  })
  it('names the default CPU', () => {
    expect(cpuModelWord('')).toBe("The node's own CPU")
  })
})
