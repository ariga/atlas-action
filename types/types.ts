export type Maybe<T> = T | null
export type InputMaybe<T> = Maybe<T>
export type Exact<T extends { [key: string]: unknown }> = {
  [K in keyof T]: T[K]
}
export type MakeOptional<T, K extends keyof T> = Omit<T, K> & {
  [SubKey in K]?: Maybe<T[SubKey]>
}
export type MakeMaybe<T, K extends keyof T> = Omit<T, K> & {
  [SubKey in K]: Maybe<T[SubKey]>
}
/** All built-in and custom scalars, mapped to their actual values */
export type Scalars = {
  ID: string
  String: string
  Boolean: boolean
  Int: number
  Float: number
  /** Maps a Bytes GraphQL scalar to a byte type. */
  Bytes: any
}

/** Input type of CreateReport */
export type CreateReportInput = {
  /** The branch of the report. */
  branch: Scalars['String']
  /** The branch of the report. */
  commit: Scalars['String']
  /** The Environment of the report. */
  envName: Scalars['String']
  /** The output of atlas lint. */
  payload?: InputMaybe<Scalars['Bytes']>
  /** The Project of the report. */
  projectName: Scalars['String']
  /** The status of the CI run. */
  status: RunStatus
  /** The URL back to the CI system. */
  url: Scalars['String']
}

/** Return type of CreateReport. */
export type CreateReportPayload = {
  __typename?: 'CreateReportPayload'
  /** List of cloud Reports. */
  cloudReports: Array<Maybe<SqlCheckReport>>
  /** The ID of the run. */
  runID: Scalars['ID']
  /** The URL for the report. */
  url: Scalars['String']
}

export type Mutation = {
  __typename?: 'Mutation'
  createReport: CreateReportPayload
}

export type MutationCreateReportArgs = {
  input: CreateReportInput
}

export type Query = {
  __typename?: 'Query'
  _dummy?: Maybe<Scalars['String']>
}

/** RunStatus is enum for the field status */
export enum RunStatus {
  Failed = 'FAILED',
  Successful = 'SUCCESSFUL',
  Unknown = 'UNKNOWN'
}

/** SQLCheckDiagnostic is a text associated with a specific position of a statement in a file. */
export type SqlCheckDiagnostic = {
  __typename?: 'SQLCheckDiagnostic'
  /** Diagnostic code. */
  code: Scalars['String']
  /** Diagnostic position. */
  pos: Scalars['Int']
  /** Diagnostic text. */
  text: Scalars['String']
}

/** SQLCheckReport describes an analysis report with an optional specific diagnostic. */
export type SqlCheckReport = {
  __typename?: 'SQLCheckReport'
  /** List of SQLCheckDiagnostic. */
  diagnostics: Array<Maybe<SqlCheckDiagnostic>>
  /** Report text. */
  text: Scalars['String']
}
