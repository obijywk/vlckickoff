'use strict';

var app = angular.module('vlckickoffApp', [
  'ngResource'
]).config([
  '$compileProvider', function ($compileProvider) {
    $compileProvider.aHrefSanitizationWhitelist(/^\s*(https?|ftp|mailto|intent):/);
  }
]);

app.factory('Stream', [
  '$resource', function ($resource) {
    return $resource('streams/:Name', {}, {}, {});
  }
]);

app.factory('Settings', [
  '$resource', function ($resource) {
    return $resource('settings/', {}, {}, {});
  }
]);

app.controller('StreamListCtrl', [
  '$scope', 'Stream', 'Settings', function ($scope, Stream, Settings) {
    $scope.settings = Settings.get();
    $scope.streams = Stream.query();

    $scope.activeStream = function() {
      var active = {active: null};
      angular.forEach($scope.streams, function(stream) {
        if (stream.Active) {
          this.active = stream;
        }
      }, active);
      return active.active;
    }

    $scope.switchStream = function(newActiveStream) {
      angular.forEach($scope.streams, function(stream) {
        stream.Active = stream == newActiveStream;
        stream.$save();
      });
    }
  }
]);
