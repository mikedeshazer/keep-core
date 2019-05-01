import { duration } from './helpers/increaseTime';
import {bls} from './helpers/data';
import mineBlocks from './helpers/mineBlocks';
import generateTickets from './helpers/generateTickets';
import stakeDelegate from './helpers/stakeDelegate';
import expectThrow from './helpers/expectThrow';
import shuffleArray from './helpers/shuffle';
const KeepToken = artifacts.require('./KeepToken.sol');
const StakingProxy = artifacts.require('./StakingProxy.sol');
const TokenStaking = artifacts.require('./TokenStaking.sol');
const KeepRandomBeaconFrontendProxy = artifacts.require('./KeepRandomBeaconFrontendProxy.sol');
const KeepRandomBeaconFrontendImplV1 = artifacts.require('./KeepRandomBeaconFrontendImplV1.sol');
const KeepRandomBeaconBackend = artifacts.require('./KeepRandomBeaconBackend.sol');


contract('TestPublishDkgResult', function(accounts) {

  const minimumStake = 200000;
  const groupThreshold = 15;
  const groupSize = 20;
  const timeoutInitial = 20;
  const timeoutSubmission = 100;
  const timeoutChallenge = 60;
  const timeDKG = 20;
  const resultPublicationBlockStep = 3;

  let disqualified, inactive, resultHash,
  token, stakingProxy, stakingContract, randomBeaconValue, requestId,
  keepRandomBeaconFrontendImplV1, keepRandomBeaconFrontendProxy, keepRandomBeaconFrontendImplViaProxy,
  keepRandomBeaconBackend, groupPubKey,
  ticketSubmissionStartBlock, selectedParticipants, signatures, signingMemberIndices = [],
  owner = accounts[0], magpie = accounts[0],
  operator1 = accounts[0], tickets1,
  operator2 = accounts[1], tickets2,
  operator3 = accounts[2], tickets3,
  operator4 = accounts[3];
  requestId = 0;
  disqualified = '0x0000000000000000000000000000000000000000'
  inactive = '0x0000000000000000000000000000000000000000'
  groupPubKey = "0x1000000000000000000000000000000000000000000000000000000000000000"

  resultHash = web3.utils.soliditySha3(groupPubKey, disqualified, inactive);

  beforeEach(async () => {
    token = await KeepToken.new();

    // Initialize staking contract under proxy
    stakingProxy = await StakingProxy.new();
    stakingContract = await TokenStaking.new(token.address, stakingProxy.address, duration.days(30));
    await stakingProxy.authorizeContract(stakingContract.address, {from: owner})

    // Initialize Keep Random Beacon contract
    keepRandomBeaconFrontendImplV1 = await KeepRandomBeaconFrontendImplV1.new();
    keepRandomBeaconFrontendProxy = await KeepRandomBeaconFrontendProxy.new(keepRandomBeaconFrontendImplV1.address);
    keepRandomBeaconFrontendImplViaProxy = await KeepRandomBeaconFrontendImplV1.at(keepRandomBeaconFrontendProxy.address);

    // Initialize Keep Random Beacon backend contract
    keepRandomBeaconBackend = await KeepRandomBeaconBackend.new();
    await keepRandomBeaconBackend.initialize(
      stakingProxy.address, keepRandomBeaconFrontendProxy.address, minimumStake, groupThreshold,
      groupSize, timeoutInitial, timeoutSubmission, timeoutChallenge, timeDKG, resultPublicationBlockStep
    );

    randomBeaconValue = bls.groupSignature;

    await keepRandomBeaconFrontendImplViaProxy.initialize(1,1, randomBeaconValue, bls.groupPubKey, keepRandomBeaconBackend.address);
    await keepRandomBeaconFrontendImplViaProxy.relayEntry(1, bls.groupSignature, bls.groupPubKey, bls.previousEntry, bls.seed);

    await stakeDelegate(stakingContract, token, owner, operator1, magpie, minimumStake*2000)
    await stakeDelegate(stakingContract, token, owner, operator2, magpie, minimumStake*2000)
    await stakeDelegate(stakingContract, token, owner, operator3, magpie, minimumStake*3000)

    tickets1 = generateTickets(randomBeaconValue, operator1, 2000);
    tickets2 = generateTickets(randomBeaconValue, operator2, 2000);
    tickets3 = generateTickets(randomBeaconValue, operator3, 3000);

    for(let i = 0; i < groupSize; i++) {
      await keepRandomBeaconBackend.submitTicket(tickets1[i].value, operator1, tickets1[i].virtualStakerIndex, {from: operator1});
    }

    for(let i = 0; i < groupSize; i++) {
      await keepRandomBeaconBackend.submitTicket(tickets2[i].value, operator2, tickets2[i].virtualStakerIndex, {from: operator2});
    }

    for(let i = 0; i < groupSize; i++) {
      await keepRandomBeaconBackend.submitTicket(tickets3[i].value, operator3, tickets3[i].virtualStakerIndex, {from: operator3});
    }

    ticketSubmissionStartBlock = await keepRandomBeaconBackend.ticketSubmissionStartBlock();
    selectedParticipants = await keepRandomBeaconBackend.selectedParticipants();

    for(let i = 0; i < selectedParticipants.length; i++) {
      let signature = await web3.eth.sign(resultHash, selectedParticipants[i]);
      signingMemberIndices.push(i+1);
      if (signatures == undefined) signatures = signature
      else signatures += signature.slice(2, signature.length);
    }
  });

  it("should be able to submit correct result as first member after DKG finished.", async function() {

    // Jump in time to when submitter becomes eligible to submit
    let currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG - currentBlock);

    await keepRandomBeaconBackend.submitDkgResult(requestId, 1, groupPubKey, disqualified, inactive, signatures, signingMemberIndices, {from: selectedParticipants[0]})
    let submitted = await keepRandomBeaconBackend.isDkgResultSubmitted.call(requestId);
    assert.equal(submitted, true, "DkgResult should should be submitted");
  });

  it("should be able to submit correct result with unordered signatures and indexes.", async function() {

    let unorderedSigningMembersIndexes = [];
    for (let i = 0; i < selectedParticipants.length; i++) {
      unorderedSigningMembersIndexes[i] = i + 1;
    }

    unorderedSigningMembersIndexes = shuffleArray(unorderedSigningMembersIndexes);
    let unorderedSignatures;

    for(let i = 0; i < selectedParticipants.length; i++) {
      let signature = await web3.eth.sign(resultHash, selectedParticipants[unorderedSigningMembersIndexes[i] - 1]);
      if (unorderedSignatures == undefined) unorderedSignatures = signature
      else unorderedSignatures += signature.slice(2, signature.length);
    }

    // Jump in time to when submitter becomes eligible to submit
    let currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG - currentBlock);

    await keepRandomBeaconBackend.submitDkgResult(requestId, 1, groupPubKey, disqualified, inactive, unorderedSignatures, unorderedSigningMembersIndexes, {from: selectedParticipants[0]})
    let submitted = await keepRandomBeaconBackend.isDkgResultSubmitted.call(requestId);
    assert.equal(submitted, true, "DkgResult should should be submitted");
  });

  it("should only be able to submit result at eligible block time based on member index.", async function() {

    let submitter1MemberIndex = 4;
    let submitter2MemberIndex = 5;
    let submitter2 = selectedParticipants[submitter2MemberIndex - 1];
    let eligibleBlockForSubmitter1 = ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG + (submitter1MemberIndex-1)*resultPublicationBlockStep;
    let eligibleBlockForSubmitter2 = ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG + (submitter2MemberIndex-1)*resultPublicationBlockStep;

    // Jump in time to when submitter 1 becomes eligible to submit
    let currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(eligibleBlockForSubmitter1 - currentBlock);

    // Should throw if non eligible submitter 2 tries to submit
    await expectThrow(keepRandomBeaconBackend.submitDkgResult(
      requestId, submitter2MemberIndex, groupPubKey, disqualified, inactive, signatures, signingMemberIndices,
      {from: submitter2})
    );

    // Jump in time to when submitter 2 becomes eligible to submit
    currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(eligibleBlockForSubmitter2 - currentBlock);

    await keepRandomBeaconBackend.submitDkgResult(requestId, submitter2MemberIndex, groupPubKey, disqualified, inactive, signatures, signingMemberIndices, {from: submitter2})
    let submitted = await keepRandomBeaconBackend.isDkgResultSubmitted.call(requestId);
    assert.equal(submitted, true, "DkgResult should be submitted");
  });

  it("should not be able to submit if submitter was not selected to be part of the group.", async function() {
    await expectThrow(keepRandomBeaconBackend.submitDkgResult(
      requestId, 1, groupPubKey, disqualified, inactive, signatures, signingMemberIndices, 
      {from: operator4})
    );
  });

  it("should reject the result with invalid signatures.", async function() {

    signingMemberIndices = [];
    signatures = undefined;
    let lastParticipantIdx = groupThreshold - 1;

    // Create less than minimum amount of valid signatures
    for(let i = 0; i < lastParticipantIdx; i++) {
      let signature = await web3.eth.sign(resultHash, selectedParticipants[i]);
      signingMemberIndices.push(i+1);
      if (signatures == undefined) signatures = signature
      else signatures += signature.slice(2, signature.length);
    }

    // Add invalid signature as the last one
    let nonsenseHash = web3.utils.soliditySha3("ducky duck");
    let invalidSignature = await web3.eth.sign(nonsenseHash, selectedParticipants[lastParticipantIdx]);
    signatures += invalidSignature.slice(2, invalidSignature.length);
    signingMemberIndices.push(lastParticipantIdx);

    // Jump in time to when first member is eligible to submit
    let currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG - currentBlock);

    await expectThrow(keepRandomBeaconBackend.submitDkgResult(
      requestId, 1, groupPubKey, disqualified, inactive, signatures, signingMemberIndices,
      {from: selectedParticipants[0]})
    );
  });

  it("should be able to submit the result with minimum number of valid signatures", async function() {

    signingMemberIndices = [];
    signatures = undefined;

    // Create minimum amount of valid signatures
    for(let i = 0; i < groupThreshold; i++) {
      let signature = await web3.eth.sign(resultHash, selectedParticipants[i]);
      signingMemberIndices.push(i+1);
      if (signatures == undefined) signatures = signature
      else signatures += signature.slice(2, signature.length);
    }

    // Jump in time to when first member is eligible to submit
    let currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG - currentBlock);

    await keepRandomBeaconBackend.submitDkgResult(
      requestId, 1, groupPubKey, disqualified, inactive, signatures, signingMemberIndices,
      {from: selectedParticipants[0]})
    let submitted = await keepRandomBeaconBackend.isDkgResultSubmitted.call(requestId);
    assert.equal(submitted, true, "DkgResult should should be submitted");

  });

  it("should not be able to submit without minimum number of signatures", async function() {

    signingMemberIndices = [];
    signatures = undefined;

    // Create less than minimum amount of valid signatures
    for(let i = 0; i < groupThreshold - 1; i++) {
      let signature = await web3.eth.sign(resultHash, selectedParticipants[i]);
      signingMemberIndices.push(i+1);
      if (signatures == undefined) signatures = signature
      else signatures += signature.slice(2, signature.length);
    }

    // Jump in time to when first member is eligible to submit
    let currentBlock = await web3.eth.getBlockNumber();
    mineBlocks(ticketSubmissionStartBlock.toNumber() + timeoutChallenge + timeDKG - currentBlock);

    await expectThrow(keepRandomBeaconBackend.submitDkgResult(
      requestId, 1, groupPubKey, disqualified, inactive, signatures, signingMemberIndices,
      {from: selectedParticipants[0]})
    );

  });
})
